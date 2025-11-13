package currency

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

type APICache struct {
	client *http.Client
	mu     sync.RWMutex

	whitebirdRates      map[string]float64
	whitebirdLastUpdate time.Time

	bybitRates      map[string]*BybitRate
	bybitLastUpdate time.Time

	mastercardRates      map[string]float64
	mastercardLastUpdate time.Time

	validCryptos map[string]bool
	validFiats   map[string]bool

	currencyMetadata map[string]*CurrencyMetadata

	tradeablePairs map[string]bool
	pairsLastCheck time.Time

	lastWhitebirdRates  map[string]float64
	lastBybitRates      map[string]*BybitRate
	lastMastercardRates map[string]float64
}

func NewAPICache() *APICache {
	validCryptos := make(map[string]bool)
	for _, c := range supportedCryptos {
		validCryptos[c] = true
	}

	validFiats := make(map[string]bool)
	for _, f := range supportedFiats {
		validFiats[f] = true
	}

	return &APICache{
		client:              &http.Client{Timeout: 30 * time.Second},
		whitebirdRates:      make(map[string]float64),
		bybitRates:          make(map[string]*BybitRate),
		mastercardRates:     make(map[string]float64),
		validCryptos:        validCryptos,
		validFiats:          validFiats,
		currencyMetadata:    make(map[string]*CurrencyMetadata),
		tradeablePairs:      make(map[string]bool),
		lastWhitebirdRates:  make(map[string]float64),
		lastBybitRates:      make(map[string]*BybitRate),
		lastMastercardRates: make(map[string]float64),
	}
}

func (ac *APICache) IsStale() bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	now := time.Now()

	if now.Sub(ac.whitebirdLastUpdate) > criticalStalenessThreshold {
		return true
	}
	if now.Sub(ac.bybitLastUpdate) > criticalStalenessThreshold {
		return true
	}
	if now.Sub(ac.mastercardLastUpdate) > criticalStalenessThreshold*4 {
		return true
	}

	return false
}

func (ac *APICache) IsCriticallyStale() bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	now := time.Now()

	if now.Sub(ac.whitebirdLastUpdate) > criticalStalenessThreshold*2 {
		return true
	}
	if now.Sub(ac.bybitLastUpdate) > criticalStalenessThreshold*2 {
		return true
	}

	return false
}

func (ac *APICache) InitializeTradeablePairs() {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	for symbol := range ac.bybitRates {
		ac.tradeablePairs[symbol] = true
	}
	ac.pairsLastCheck = time.Now()
}

func (ac *APICache) IsCrypto(code string) bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	return ac.validCryptos[code]
}

func (ac *APICache) IsFiat(code string) bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	return ac.validFiats[code]
}

func (ac *APICache) GetCacheStaleness() map[string]time.Duration {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	now := time.Now()
	return map[string]time.Duration{
		"whitebird":  now.Sub(ac.whitebirdLastUpdate),
		"bybit":      now.Sub(ac.bybitLastUpdate),
		"mastercard": now.Sub(ac.mastercardLastUpdate),
	}
}

func (ac *APICache) ValidateWhitebirdRates() bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	rubToTonBuy, ok1 := ac.whitebirdRates["RUB_TON_BUY"]
	tonToRubSell, ok2 := ac.whitebirdRates["TON_RUB_SELL"]

	if !ok1 || !ok2 || !isValidFloat(rubToTonBuy) || !isValidFloat(tonToRubSell) {
		return false
	}

	if rubToTonBuy < whitebirdRateMin || rubToTonBuy > whitebirdRateMax {
		return false
	}
	if tonToRubSell < whitebirdRateMin || tonToRubSell > whitebirdRateMax {
		return false
	}

	spread := (rubToTonBuy - tonToRubSell) / rubToTonBuy
	return spread > whitebirdMinSpread && spread < whitebirdMaxSpread
}

func (ac *APICache) InitialFetch() error {
	var wg sync.WaitGroup
	var errWhitebird, errBybit, errMastercard error

	wg.Add(3)

	go func() {
		defer wg.Done()
		errWhitebird = retryWithBackoff(ac.fetchWhitebirdRates)
	}()

	go func() {
		defer wg.Done()
		errBybit = retryWithBackoff(ac.fetchBybitRates)
	}()

	go func() {
		defer wg.Done()
		errMastercard = retryWithBackoff(ac.fetchMastercardRates)
	}()

	wg.Wait()

	if errWhitebird != nil {
		log.Printf("Warning: Whitebird fetch failed: %v", errWhitebird)
	}
	if errBybit != nil {
		log.Printf("Warning: Bybit fetch failed: %v", errBybit)
	}
	if errMastercard != nil {
		log.Printf("Warning: Mastercard fetch failed: %v", errMastercard)
	}

	if errWhitebird != nil && errBybit != nil {
		return fmt.Errorf("critical providers failed")
	}

	ac.refreshTradeablePairs()
	return nil
}

func (ac *APICache) StartBackgroundUpdaters() {
	log.Println("Starting background currency updaters...")
	go ac.updateLoop("whitebird", backgroundUpdateTTL, ac.fetchWhitebirdRates)
	go ac.updateLoop("bybit", backgroundUpdateTTL, ac.fetchBybitRates)
	go ac.updateLoop("mastercard", backgroundUpdateTTL*3, ac.fetchMastercardRates)
}

func (ac *APICache) updateLoop(name string, interval time.Duration, fetchFn func() error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		if err := retryWithBackoff(fetchFn); err != nil {
			log.Printf("ERROR: %s background update failed: %v", name, err)
		}
	}
}

func (ac *APICache) ForceRefresh() error {
	log.Println("Force refreshing all rates...")
	var wg sync.WaitGroup
	var errWhitebird, errBybit error

	wg.Add(3)

	go func() {
		defer wg.Done()
		errWhitebird = retryWithBackoff(ac.fetchWhitebirdRates)
	}()

	go func() {
		defer wg.Done()
		errBybit = retryWithBackoff(ac.fetchBybitRates)
	}()

	go func() {
		defer wg.Done()
		_ = retryWithBackoff(ac.fetchMastercardRates)
	}()

	wg.Wait()

	if errWhitebird != nil && errBybit != nil {
		return fmt.Errorf("critical providers failed during refresh")
	}

	return nil
}

func (ac *APICache) refreshTradeablePairs() {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	for symbol := range ac.bybitRates {
		ac.tradeablePairs[symbol] = true
	}
	ac.pairsLastCheck = time.Now()
}

func retryWithBackoff(fn func() error) error {
	var lastErr error
	delay := baseRetryDelay

	for i := 0; i < maxRetries; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
			if i < maxRetries-1 {
				time.Sleep(delay)
				delay *= 2
			}
		}
	}

	return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}
