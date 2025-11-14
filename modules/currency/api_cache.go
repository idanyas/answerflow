package currency

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// ============================================================================
// Circuit Breaker
// ============================================================================

const (
	circuitBreakerThreshold = 5
	circuitBreakerTimeout   = 5 * time.Minute
)

type CircuitBreaker struct {
	mu           sync.RWMutex
	failures     int
	lastFailTime time.Time
	state        string
	openUntil    time.Time
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.lastFailTime = time.Now()

	if cb.failures >= circuitBreakerThreshold {
		cb.state = "open"
		cb.openUntil = time.Now().Add(circuitBreakerTimeout)
	}
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.failures > 0 {
		cb.failures--
	}
	if cb.failures == 0 {
		cb.state = "closed"
	}
}

func (cb *CircuitBreaker) CanAttempt() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == "open" {
		if time.Now().After(cb.openUntil) {
			cb.state = "closed"
			cb.failures = 0
			return true
		}
		return false
	}
	return true
}

var (
	whitebirdCircuit  = &CircuitBreaker{state: "closed"}
	bybitCircuit      = &CircuitBreaker{state: "closed"}
	mastercardCircuit = &CircuitBreaker{state: "closed"}
)

// ============================================================================
// Conversion Cache
// ============================================================================

const calculationCacheTTL = 5 * time.Minute

type ConversionCache struct {
	mu          sync.RWMutex
	results     map[string]*CachedConversion
	lastCleanup time.Time
}

type CachedConversion struct {
	value     float64
	timestamp time.Time
}

var globalConversionCache = &ConversionCache{
	results:     make(map[string]*CachedConversion),
	lastCleanup: time.Now(),
}

func (c *ConversionCache) Get(key string) (float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if result, ok := c.results[key]; ok {
		if time.Since(result.timestamp) < calculationCacheTTL {
			return result.value, true
		}
	}
	return 0, false
}

func (c *ConversionCache) Set(key string, value float64) {
	if !isValidFloat(value) {
		return
	}

	c.mu.Lock()
	c.results[key] = &CachedConversion{value, time.Now()}

	if time.Since(c.lastCleanup) > 5*time.Minute {
		c.cleanup()
	}
	c.mu.Unlock()
}

func (c *ConversionCache) cleanup() {
	now := time.Now()
	for k, v := range c.results {
		if now.Sub(v.timestamp) > calculationCacheTTL*2 {
			delete(c.results, k)
		}
	}
	c.lastCleanup = now
}

func formatCacheKey(from, to string, amount float64) string {
	var bucket string
	switch {
	case amount < 10:
		bucket = "S"
	case amount < 100:
		bucket = "M"
	case amount < 1000:
		bucket = "L"
	case amount < 10000:
		bucket = "XL"
	default:
		bucket = "XXL"
	}
	return fmt.Sprintf("%s_%s_%s", from, to, bucket)
}

// ============================================================================
// API Cache
// ============================================================================

type APICache struct {
	client *http.Client
	mu     sync.RWMutex

	whitebirdRates      map[string]float64
	whitebirdLastUpdate time.Time
	lastWhitebirdRates  map[string]float64

	bybitRates      map[string]*BybitRate
	bybitLastUpdate time.Time
	lastBybitRates  map[string]*BybitRate

	mastercardRates      map[string]float64
	mastercardLastUpdate time.Time
	lastMastercardRates  map[string]float64

	validCryptos     map[string]bool
	validFiats       map[string]bool
	currencyMetadata map[string]*CurrencyMetadata
	tradeablePairs   map[string]bool
	pairsLastCheck   time.Time
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

func (ac *APICache) GetWhitebirdRate(from, to string) (float64, error) {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	var key string
	if from == "RUB" && to == "TON" {
		key = "RUB_TON_BUY"
	} else if from == "TON" && to == "RUB" {
		key = "TON_RUB_SELL"
	} else {
		return 0, fmt.Errorf("unsupported pair")
	}

	rate, ok := ac.whitebirdRates[key]
	if !ok || !isValidFloat(rate) {
		return 0, fmt.Errorf("rate not available")
	}
	return rate, nil
}

func (ac *APICache) GetBybitRate(symbol string) (*BybitRate, error) {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	rate, ok := ac.bybitRates[symbol]
	if !ok || rate == nil || !isValidFloat(rate.BestBid) || !isValidFloat(rate.BestAsk) {
		return nil, fmt.Errorf("rate not available")
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

	if from == "USD" {
		key := fmt.Sprintf("USD_%s", to)
		rate, ok := ac.mastercardRates[key]
		if !ok || !isValidFloat(rate) {
			return 0, fmt.Errorf("rate not available")
		}
		return rate, nil
	}

	if to == "USD" {
		key := fmt.Sprintf("USD_%s", from)
		rate, ok := ac.mastercardRates[key]
		if !ok || !isValidFloat(rate) {
			return 0, fmt.Errorf("rate not available")
		}
		return 1.0 / rate, nil
	}

	fromKey := fmt.Sprintf("USD_%s", from)
	toKey := fmt.Sprintf("USD_%s", to)
	fromRate, okFrom := ac.mastercardRates[fromKey]
	toRate, okTo := ac.mastercardRates[toKey]

	if !okFrom || !okTo || !isValidFloat(fromRate) || !isValidFloat(toRate) {
		return 0, fmt.Errorf("cross rate not available")
	}
	return toRate / fromRate, nil
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
			log.Printf("ERROR: %s update failed: %v", name, err)
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
		return fmt.Errorf("critical providers failed")
	}
	return nil
}
