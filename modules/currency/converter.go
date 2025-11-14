package currency

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

type ConversionCache struct {
	results map[string]*cachedValue
	mu      sync.RWMutex
}

type cachedValue struct {
	value     float64
	timestamp time.Time
}

var globalConversionCache = &ConversionCache{
	results: make(map[string]*cachedValue),
}

func (c *ConversionCache) Get(key string) (float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result, ok := c.results[key]
	if !ok || time.Since(result.timestamp) >= calculationCacheTTL {
		return 0, false
	}
	return result.value, true
}

func (c *ConversionCache) Set(key string, value float64) {
	if !isValidFloat(value) {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.results) >= maxCacheSize {
		for k, v := range c.results {
			if time.Since(v.timestamp) > calculationCacheTTL*2 {
				delete(c.results, k)
			}
		}
	}

	c.results[key] = &cachedValue{value, time.Now()}
}

func (m *CurrencyConverterModule) convert(amount float64, from, to string, apiCache *APICache) (float64, error) {
	if from == to {
		return amount, nil
	}

	if err := ValidateAmount(amount); err != nil {
		return 0, err
	}

	if apiCache.IsStale() {
		staleness := apiCache.GetCacheStaleness()
		for _, duration := range staleness {
			if duration > circuitBreakerTimeout {
				return 0, fmt.Errorf("exchange rates outdated, please try again")
			}
		}
	}

	if from == CurrencyUSDT && to == CurrencyUSD {
		return amount * (1 - feeUSDTToUSD), nil
	}
	if from == CurrencyUSD && to == CurrencyUSDT {
		return amount * (1 - feeUSDToUSDT), nil
	}

	cacheKey := formatCacheKey(from, to, amount)
	if cached, ok := globalConversionCache.Get(cacheKey); ok {
		return cached, nil
	}

	result, err := m.routeConversion(amount, from, to, apiCache)
	if err != nil {
		return 0, err
	}

	if !isValidFloat(result) {
		return 0, fmt.Errorf("invalid conversion result")
	}

	globalConversionCache.Set(cacheKey, result)
	return result, nil
}

func getCurrencyType(code string, apiCache *APICache) string {
	switch code {
	case "RUB":
		return "RUB"
	case "TON":
		return "TON"
	}
	if apiCache.IsCrypto(code) {
		return "crypto"
	}
	if apiCache.IsFiat(code) {
		return "fiat"
	}
	return "unknown"
}

func (m *CurrencyConverterModule) findInverseAmount(targetAmount float64, sourceCurrency, targetCurrency string, apiCache *APICache) (float64, error) {
	if err := ValidateAmount(targetAmount); err != nil {
		return 0, err
	}

	cacheKey := formatCacheKey("inverse_"+sourceCurrency, targetCurrency, targetAmount)
	if cached, ok := globalConversionCache.Get(cacheKey); ok {
		return cached, nil
	}

	testAmount := 1.0
	if sourceCurrency == CurrencyRUB || sourceCurrency == CurrencyTON {
		testAmount = 1000.0
		switch targetCurrency {
		case CurrencyTON:
			testAmount = 1000.0
		case CurrencyRUB:
			testAmount = 10.0
		}
	}

	resultFromTest, err := m.convert(testAmount, sourceCurrency, targetCurrency, apiCache)
	if err != nil || resultFromTest <= 0 {
		return 0, fmt.Errorf("failed to get rate")
	}

	effectiveRate := resultFromTest / testAmount
	sourceNeeded := targetAmount / effectiveRate

	if sourceCurrency == CurrencyRUB || sourceCurrency == CurrencyTON || targetCurrency == CurrencyRUB || targetCurrency == CurrencyTON {
		maxIterations := 3
		tolerance := 0.01

		for i := 0; i < maxIterations; i++ {
			actualResult, err := m.convert(sourceNeeded, sourceCurrency, targetCurrency, apiCache)
			if err != nil {
				if i == 0 {
					break
				}
				break
			}

			deviation := (actualResult - targetAmount) / targetAmount
			if deviation > -tolerance && deviation < tolerance {
				break
			}

			correctionFactor := targetAmount / actualResult
			sourceNeeded = sourceNeeded * correctionFactor

			if i == maxIterations-1 {
				log.Printf("Inverse converged to %.6f %s after %d iterations", sourceNeeded, sourceCurrency, i+1)
			}
		}
	}

	if err := ValidateAmount(sourceNeeded); err != nil {
		return 0, err
	}

	globalConversionCache.Set(cacheKey, sourceNeeded)
	return sourceNeeded, nil
}

func retryWithBackoff(ctx context.Context, fn func() error) error {
	var lastErr error
	delay := baseRetryDelay

	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
			if i < maxRetries-1 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
				delay = time.Duration(float64(delay) * 2)
				if delay > maxRetryDelay {
					delay = maxRetryDelay
				}
			}
		}
	}
	return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}
