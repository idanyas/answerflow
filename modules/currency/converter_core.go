package currency

import (
	"fmt"
	"log"
	"math"
	"time"
)

// convert performs the actual conversion logic with order book depth support
func (m *CurrencyConverterModule) convert(amount float64, from, to string, apiCache *APICache) (float64, error) {
	if from == to {
		return amount, nil
	}

	if err := ValidateAmount(amount); err != nil {
		return 0, err
	}

	if apiCache.IsStale() {
		staleness := apiCache.GetCacheStaleness()
		for provider, duration := range staleness {
			if duration > circuitBreakerTimeout {
				return 0, fmt.Errorf("%s data is critically stale (%v old)", provider, duration)
			}
		}
	}

	// Direct USDT <-> USD
	if from == "USDT" && to == "USD" {
		return amount * (1 - feeUSDTToUSD), nil
	}
	if from == "USD" && to == "USDT" {
		return amount * (1 - feeUSDToUSDT), nil
	}

	cacheKey := formatCacheKey(from, to, amount)
	if cached, ok := globalConversionCache.Get(cacheKey); ok {
		return cached, nil
	}

	result, err := m.routeConversion(amount, from, to, apiCache)
	if err == nil && isValidFloat(result) {
		globalConversionCache.Set(cacheKey, result)
	}

	return result, err
}

// routeConversion routes to appropriate conversion function
func (m *CurrencyConverterModule) routeConversion(amount float64, from, to string, apiCache *APICache) (float64, error) {
	fromType := getCurrencyType(from, apiCache)
	toType := getCurrencyType(to, apiCache)

	switch {
	case fromType == "RUB" && toType == "TON":
		return m.convertRUBToTONDirect(amount, apiCache)
	case fromType == "TON" && toType == "RUB":
		return m.convertTONToRUBDirect(amount, apiCache)
	case fromType == "RUB" && toType == "crypto":
		return m.convertViaRoute(amount, from, to, apiCache, []string{"TON", "USDT"})
	case fromType == "RUB" && toType == "fiat":
		return m.convertViaRoute(amount, from, to, apiCache, []string{"TON", "USDT", "USD"})
	case fromType == "crypto" && toType == "RUB":
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USDT", "TON"})
	case fromType == "fiat" && toType == "RUB":
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USD", "USDT", "TON"})
	case fromType == "crypto" && toType == "crypto":
		return m.convertCryptoPair(amount, from, to, apiCache)
	case fromType == "fiat" && toType == "fiat":
		return m.convertFiatPair(amount, from, to, apiCache)
	case fromType == "TON" && toType == "crypto":
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USDT"})
	case fromType == "crypto" && toType == "TON":
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USDT"})
	case fromType == "TON" && toType == "fiat":
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USDT", "USD"})
	case fromType == "fiat" && toType == "TON":
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USD", "USDT"})
	default:
		return 0, fmt.Errorf("unsupported conversion: %s (%s) -> %s (%s)", from, fromType, to, toType)
	}
}

// convertViaRoute performs multi-step conversion through intermediate currencies
func (m *CurrencyConverterModule) convertViaRoute(amount float64, from, to string, apiCache *APICache, route []string) (float64, error) {
	current := amount
	currentCurrency := from

	for _, intermediate := range route {
		if currentCurrency == intermediate {
			continue
		}

		var err error
		current, err = m.convertDirectPair(current, currentCurrency, intermediate, apiCache)
		if err != nil {
			return 0, fmt.Errorf("%s->%s failed: %w", currentCurrency, intermediate, err)
		}

		currentCurrency = intermediate
		log.Printf("Route step: %s = %f %s", from, current, currentCurrency)
	}

	if currentCurrency != to {
		var err error
		current, err = m.convertDirectPair(current, currentCurrency, to, apiCache)
		if err != nil {
			return 0, fmt.Errorf("%s->%s failed: %w", currentCurrency, to, err)
		}
	}

	return current, nil
}

// convertDirectPair handles direct pair conversion
func (m *CurrencyConverterModule) convertDirectPair(amount float64, from, to string, apiCache *APICache) (float64, error) {
	if from == to {
		return amount, nil
	}

	fromType := getCurrencyType(from, apiCache)
	toType := getCurrencyType(to, apiCache)

	switch {
	case from == "TON" && to == "USDT":
		return m.convertTONToUSDT(amount, apiCache)
	case from == "USDT" && to == "TON":
		return m.convertUSDTToTON(amount, apiCache)
	case from == "USDT" && to == "USD":
		return amount * (1 - feeUSDTToUSD), nil
	case from == "USD" && to == "USDT":
		return amount * (1 - feeUSDToUSDT), nil
	case fromType == "crypto" && to == "USDT":
		return m.convertCryptoToUSDT(amount, from, apiCache)
	case from == "USDT" && toType == "crypto":
		return m.convertUSDTToCrypto(amount, to, apiCache)
	case fromType == "fiat" && to == "USD":
		return m.convertFiatToUSD(amount, from, apiCache)
	case from == "USD" && toType == "fiat":
		return m.convertUSDToFiat(amount, to, apiCache)
	default:
		return 0, fmt.Errorf("unsupported direct pair: %s -> %s", from, to)
	}
}

// getCurrencyType determines currency type
func getCurrencyType(code string, apiCache *APICache) string {
	if code == "RUB" {
		return "RUB"
	}
	if code == "TON" {
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

// findInverseAmount calculates inverse conversion
func (m *CurrencyConverterModule) findInverseAmount(targetAmount float64, sourceCurrency, targetCurrency string, apiCache *APICache) (float64, error) {
	if err := ValidateAmount(targetAmount); err != nil {
		return 0, err
	}

	cacheKey := formatCacheKey("inverse_"+sourceCurrency, targetCurrency, targetAmount)
	if cached, ok := globalConversionCache.Get(cacheKey); ok {
		return cached, nil
	}

	testRate, err := m.convert(1.0, sourceCurrency, targetCurrency, apiCache)
	if err != nil || testRate <= 0 {
		return 0, fmt.Errorf("failed to get base rate: %w", err)
	}

	estimate := targetAmount / testRate
	low, high := estimate*0.1, estimate*10.0
	tolerance := math.Max(targetAmount*0.00001, 0.000001)

	for i := 0; i < 150; i++ {
		mid := (low + high) / 2.0
		result, err := m.convert(mid, sourceCurrency, targetCurrency, apiCache)
		if err != nil {
			return 0, err
		}

		if math.Abs(result-targetAmount) < tolerance {
			globalConversionCache.Set(cacheKey, mid)
			return mid, nil
		}

		if result < targetAmount {
			low = mid
		} else {
			high = mid
		}

		if math.Abs(high-low) < 0.000001 {
			break
		}
	}

	finalAmount := (low + high) / 2.0
	if err := ValidateAmount(finalAmount); err != nil {
		return 0, err
	}

	globalConversionCache.Set(cacheKey, finalAmount)
	return finalAmount, nil
}

// retryConversion wraps conversion with retry logic
func retryConversion(fn func() (float64, error)) (float64, error) {
	var lastErr error
	for i := 0; i < conversionMaxRetries; i++ {
		result, err := fn()
		if err == nil {
			if err := ValidateConversionResult(result, "conversion"); err != nil {
				return 0, err
			}
			return result, nil
		}
		lastErr = err
		if i < conversionMaxRetries-1 {
			time.Sleep(conversionRetryDelay * time.Duration(i+1))
		}
	}
	return 0, fmt.Errorf("conversion failed after %d retries: %w", conversionMaxRetries, lastErr)
}
