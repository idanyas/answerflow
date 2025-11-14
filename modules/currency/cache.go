package currency

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type ProviderStatus struct {
	Available        bool
	LastUpdate       time.Time
	LastError        error
	ConsecutiveFails int
}

type APICache struct {
	client *http.Client
	mu     sync.RWMutex

	// Bybit data
	bybitRates      map[string]*BybitRate
	bybitLastUpdate time.Time
	lastBybitRates  map[string]*BybitRate
	bybitStatus     ProviderStatus

	// Mastercard data
	mastercardRates      map[string]float64
	mastercardLastUpdate time.Time
	lastMastercardRates  map[string]float64
	mastercardStatus     ProviderStatus

	// Whitebird status (no pre-cached rates - always query per-amount)
	whitebirdStatus ProviderStatus

	// Metadata
	validCryptos     map[string]bool
	validFiats       map[string]bool
	currencyMetadata map[string]*CurrencyMetadata
	tradeablePairs   map[string]bool
	pairsLastCheck   time.Time

	// Symbol fetching tracking
	symbolsFetching map[string]bool

	// Health monitoring
	healthTicker      *time.Ticker
	healthStopChan    chan struct{}
	healthStopOnce    sync.Once
	refreshInProgress atomic.Bool
	bybitHealthy      atomic.Bool
	mastercardHealthy atomic.Bool
	whitebirdHealthy  atomic.Bool

	// Shutdown
	shutdownChan chan struct{}
	shutdownOnce sync.Once
}

func NewAPICache() *APICache {
	validCryptos := make(map[string]bool, len(supportedCryptos))
	for _, c := range supportedCryptos {
		validCryptos[c] = true
	}

	validFiats := make(map[string]bool, len(supportedFiats))
	for _, f := range supportedFiats {
		validFiats[f] = true
	}

	ac := &APICache{
		client:              CreateHTTPClient(),
		bybitRates:          make(map[string]*BybitRate),
		mastercardRates:     make(map[string]float64),
		validCryptos:        validCryptos,
		validFiats:          validFiats,
		currencyMetadata:    make(map[string]*CurrencyMetadata),
		tradeablePairs:      make(map[string]bool),
		lastBybitRates:      make(map[string]*BybitRate),
		lastMastercardRates: make(map[string]float64),
		symbolsFetching:     make(map[string]bool),
		bybitStatus:         ProviderStatus{Available: false},
		mastercardStatus:    ProviderStatus{Available: false},
		whitebirdStatus:     ProviderStatus{Available: false},
		healthStopChan:      make(chan struct{}),
		shutdownChan:        make(chan struct{}),
	}

	ac.bybitHealthy.Store(false)
	ac.mastercardHealthy.Store(false)
	ac.whitebirdHealthy.Store(false)

	return ac
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

func (ac *APICache) IsStale() bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	now := time.Now()
	if now.Sub(ac.bybitLastUpdate) > criticalStalenessThreshold {
		return true
	}
	if now.Sub(ac.mastercardLastUpdate) > criticalStalenessThreshold*4 {
		return true
	}
	return false
}

func (ac *APICache) GetCacheStaleness() map[string]time.Duration {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	now := time.Now()
	return map[string]time.Duration{
		"bybit":      now.Sub(ac.bybitLastUpdate),
		"mastercard": now.Sub(ac.mastercardLastUpdate),
	}
}

func (ac *APICache) InitializeTradeablePairs() {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	for symbol := range ac.bybitRates {
		ac.tradeablePairs[symbol] = true
	}
	ac.pairsLastCheck = time.Now()
}

func (ac *APICache) IsTradeablePair(symbol string) bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	if time.Since(ac.pairsLastCheck) > time.Hour {
		go ac.refreshTradeablePairs()
	}
	return ac.tradeablePairs[symbol]
}

func (ac *APICache) refreshTradeablePairs() {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	for symbol := range ac.bybitRates {
		ac.tradeablePairs[symbol] = true
	}
	ac.pairsLastCheck = time.Now()
}

func (ac *APICache) GetCurrencyMetadata(code string) *CurrencyMetadata {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	if meta, ok := ac.currencyMetadata[code]; ok {
		return meta
	}
	return &CurrencyMetadata{
		DecimalPlaces:    GetCurrencyDecimalPlaces(code),
		MinTradingAmount: 0.000001,
		MaxTradingAmount: 1000000,
	}
}

func (ac *APICache) GetBybitRate(symbol string) (*BybitRate, error) {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	if !ac.bybitStatus.Available {
		return nil, fmt.Errorf("bybit service unavailable")
	}

	rate, ok := ac.bybitRates[symbol]
	if !ok || rate == nil || !isValidFloat(rate.BestBid) || !isValidFloat(rate.BestAsk) {
		return nil, fmt.Errorf("exchange rate not available for %s", symbol)
	}

	return &BybitRate{
		BestBid:       rate.BestBid,
		BestAsk:       rate.BestAsk,
		OrderBookBids: rate.OrderBookBids,
		OrderBookAsks: rate.OrderBookAsks,
		LastUpdate:    rate.LastUpdate,
	}, nil
}

func (ac *APICache) GetBybitRateForAmount(symbol string, amount float64, isBuy bool) (float64, error) {
	avgPrice, err := ac.CalculateAverageExecutionPrice(symbol, amount, isBuy)
	if err != nil {
		rate, err := ac.GetBybitRate(symbol)
		if err != nil {
			return 0, err
		}
		if isBuy {
			return rate.BestAsk, nil
		}
		return rate.BestBid, nil
	}
	return avgPrice, nil
}

func (ac *APICache) GetMastercardRate(from, to string) (float64, error) {
	if from == to {
		return 1.0, nil
	}

	ac.mu.RLock()
	defer ac.mu.RUnlock()

	if !ac.mastercardStatus.Available {
		return 0, fmt.Errorf("fiat exchange rates temporarily unavailable")
	}

	if from == CurrencyUSD {
		key := fmt.Sprintf("USD_%s", to)
		rate, ok := ac.mastercardRates[key]
		if !ok || !isValidFloat(rate) {
			return 0, fmt.Errorf("exchange rate not available for %s", to)
		}
		return rate, nil
	}

	if to == CurrencyUSD {
		key := fmt.Sprintf("USD_%s", from)
		rate, ok := ac.mastercardRates[key]
		if !ok || !isValidFloat(rate) {
			return 0, fmt.Errorf("exchange rate not available for %s", from)
		}
		return 1.0 / rate, nil
	}

	fromKey := fmt.Sprintf("USD_%s", from)
	toKey := fmt.Sprintf("USD_%s", to)
	fromRate, okFrom := ac.mastercardRates[fromKey]
	toRate, okTo := ac.mastercardRates[toKey]

	if !okFrom || !okTo || !isValidFloat(fromRate) || !isValidFloat(toRate) {
		return 0, fmt.Errorf("exchange rate not available for %s or %s", from, to)
	}
	return toRate / fromRate, nil
}

func (ac *APICache) InitialFetch() error {
	// Try loading from persisted cache first
	if err := ac.LoadFromFile(); err != nil {
		// Log but don't fail - we'll fetch fresh data
		fmt.Printf("Warning: Could not load cached data: %v\n", err)
	}

	var wg sync.WaitGroup
	var errBybit, errMastercard error

	wg.Add(2)

	go func() {
		defer wg.Done()
		errBybit = retryWithBackoff(context.Background(), ac.fetchBybitRates)
		ac.mu.Lock()
		if errBybit != nil {
			ac.bybitStatus.Available = false
			ac.bybitStatus.LastError = errBybit
			ac.bybitStatus.ConsecutiveFails++
			ac.bybitHealthy.Store(false)
		} else {
			ac.bybitStatus.Available = true
			ac.bybitStatus.LastError = nil
			ac.bybitStatus.ConsecutiveFails = 0
			ac.bybitStatus.LastUpdate = time.Now()
			ac.bybitHealthy.Store(true)
		}
		ac.mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		errMastercard = retryWithBackoff(context.Background(), ac.fetchMastercardRates)
		ac.mu.Lock()
		if errMastercard != nil {
			ac.mastercardStatus.Available = false
			ac.mastercardStatus.LastError = errMastercard
			ac.mastercardStatus.ConsecutiveFails++
			ac.mastercardHealthy.Store(false)
		} else {
			ac.mastercardStatus.Available = true
			ac.mastercardStatus.LastError = nil
			ac.mastercardStatus.ConsecutiveFails = 0
			ac.mastercardStatus.LastUpdate = time.Now()
			ac.mastercardHealthy.Store(true)
		}
		ac.mu.Unlock()
	}()

	wg.Wait()

	ac.mu.Lock()
	ac.whitebirdStatus.Available = true
	ac.whitebirdHealthy.Store(true)
	ac.mu.Unlock()

	// Save to file after initial fetch (async, non-blocking)
	ac.SaveToFileAsync()

	if errBybit != nil {
		return fmt.Errorf("critical provider Bybit failed: %w", errBybit)
	}

	ac.refreshTradeablePairs()
	return nil
}

func (ac *APICache) IsWhitebirdAvailable() bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	return ac.whitebirdStatus.Available && whitebirdCircuit.CanAttempt()
}

func (ac *APICache) IsMastercardAvailable() bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	return ac.mastercardStatus.Available
}

func (ac *APICache) Shutdown() {
	ac.shutdownOnce.Do(func() {
		close(ac.shutdownChan)
		ac.StopHealthMonitoring()

		// Save final state before shutdown
		if err := ac.SaveToFile(); err != nil {
			fmt.Printf("Warning: Failed to save cache on shutdown: %v\n", err)
		}
	})
}
