package currency

import (
	"fmt"
	"math"
)

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
				return 0, fmt.Errorf("data critically stale")
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

func (m *CurrencyConverterModule) convertRUBToTON(amount float64, apiCache *APICache) (float64, error) {
	rate, err := apiCache.GetWhitebirdRate("RUB", "TON")
	if err != nil {
		return 0, err
	}

	ton := amount/rate - feeTONWithdrawToBybit
	return ton, ValidateConversionResult(ton, "RUB->TON")
}

func (m *CurrencyConverterModule) convertTONToRUB(amount float64, apiCache *APICache) (float64, error) {
	ton := amount - feeTONWithdrawToWhitebird
	if ton <= 0 {
		return 0, fmt.Errorf("amount too small")
	}

	rate, err := apiCache.GetWhitebirdRate("TON", "RUB")
	if err != nil {
		return 0, err
	}

	result := ton * rate
	return result, ValidateConversionResult(result, "TON->RUB")
}

func (m *CurrencyConverterModule) convertTONToUSDT(amount float64, apiCache *APICache) (float64, error) {
	rate, err := apiCache.GetBybitRate("TONUSDT")
	if err != nil {
		return 0, err
	}

	var gross float64
	if shouldUseOrderBook(amount, "TON", "USDT", apiCache) {
		avgPrice, err := apiCache.GetBybitRateForAmount("TONUSDT", amount, false)
		if err != nil {
			gross = amount * rate.BestBid
		} else {
			gross = amount * avgPrice
		}
	} else {
		gross = amount * rate.BestBid
	}

	result := gross * (1 - feeBybitTrade)
	return result, ValidateConversionResult(result, "TON->USDT")
}

func (m *CurrencyConverterModule) convertUSDTToTON(usdt float64, apiCache *APICache) (float64, error) {
	var ton float64

	if shouldUseOrderBook(usdt, "USDT", "TON", apiCache) {
		t, _, err := apiCache.CalculateBuyAmountWithUSDT("TONUSDT", usdt)
		if err != nil {
			rate, err := apiCache.GetBybitRate("TONUSDT")
			if err != nil {
				return 0, err
			}
			ton = usdt / rate.BestAsk
		} else {
			ton = t
		}
	} else {
		rate, err := apiCache.GetBybitRate("TONUSDT")
		if err != nil {
			return 0, err
		}
		ton = usdt / rate.BestAsk
	}

	result := ton * (1 - feeBybitTrade)
	return result, ValidateConversionResult(result, "USDT->TON")
}

func (m *CurrencyConverterModule) convertCryptoPair(amount float64, from, to string, apiCache *APICache) (float64, error) {
	if from == "USDT" {
		return m.convertUSDTToCrypto(amount, to, apiCache)
	}
	if to == "USDT" {
		return m.convertCryptoToUSDT(amount, from, apiCache)
	}

	usdt, err := m.convertCryptoToUSDT(amount, from, apiCache)
	if err != nil {
		return 0, err
	}
	return m.convertUSDTToCrypto(usdt, to, apiCache)
}

func (m *CurrencyConverterModule) convertFiatPair(amount float64, from, to string, apiCache *APICache) (float64, error) {
	if from == to {
		return amount, nil
	}

	usd, err := m.convertFiatToUSD(amount, from, apiCache)
	if err != nil {
		return 0, err
	}

	if to == "USD" {
		return usd, nil
	}
	return m.convertUSDToFiat(usd, to, apiCache)
}

func (m *CurrencyConverterModule) convertFiatToUSD(amount float64, from string, apiCache *APICache) (float64, error) {
	if from == "USD" {
		return amount, nil
	}

	rate, err := apiCache.GetMastercardRate(from, "USD")
	if err != nil {
		return 0, err
	}

	result := amount * rate * (1 - feeMastercard)
	return result, ValidateConversionResult(result, "fiat->USD")
}

func (m *CurrencyConverterModule) convertUSDToFiat(amount float64, to string, apiCache *APICache) (float64, error) {
	if to == "USD" {
		return amount, nil
	}

	rate, err := apiCache.GetMastercardRate("USD", to)
	if err != nil {
		return 0, err
	}

	result := amount * rate * (1 - feeMastercard)
	return result, ValidateConversionResult(result, "USD->fiat")
}

func (m *CurrencyConverterModule) convertUSDTToCrypto(usdt float64, to string, apiCache *APICache) (float64, error) {
	symbol := to + "USDT"

	if !apiCache.IsTradeablePair(symbol) {
		return 0, fmt.Errorf("not tradeable")
	}

	var crypto float64
	if shouldUseOrderBook(usdt, "USDT", to, apiCache) {
		c, _, err := apiCache.CalculateBuyAmountWithUSDT(symbol, usdt)
		if err != nil {
			rate, err := apiCache.GetBybitRate(symbol)
			if err != nil {
				return 0, err
			}
			crypto = usdt / rate.BestAsk
		} else {
			crypto = c
		}
	} else {
		rate, err := apiCache.GetBybitRate(symbol)
		if err != nil {
			return 0, err
		}
		crypto = usdt / rate.BestAsk
	}

	result := crypto * (1 - feeBybitTrade)
	return result, ValidateConversionResult(result, "USDT->"+to)
}

func (m *CurrencyConverterModule) convertCryptoToUSDT(amount float64, from string, apiCache *APICache) (float64, error) {
	symbol := from + "USDT"

	if !apiCache.IsTradeablePair(symbol) {
		return 0, fmt.Errorf("not tradeable")
	}

	rate, err := apiCache.GetBybitRate(symbol)
	if err != nil {
		return 0, err
	}

	var gross float64
	if shouldUseOrderBook(amount*rate.BestBid, "CRYPTO", "USDT", apiCache) {
		avgPrice, err := apiCache.GetBybitRateForAmount(symbol, amount, false)
		if err != nil {
			gross = amount * rate.BestBid
		} else {
			gross = amount * avgPrice
		}
	} else {
		gross = amount * rate.BestBid
	}

	result := gross * (1 - feeBybitTrade)
	return result, ValidateConversionResult(result, from+"->USDT")
}

func shouldUseOrderBook(amount float64, from, to string, apiCache *APICache) bool {
	if !isValidFloat(amount) {
		return false
	}

	fromType := getCurrencyType(from, apiCache)
	toType := getCurrencyType(to, apiCache)

	if fromType != "crypto" && toType != "crypto" && fromType != "TON" && toType != "TON" {
		return false
	}

	var usdValue float64
	if from == "USDT" || from == "USD" {
		usdValue = amount
	} else if from == "TON" {
		if rate, err := apiCache.GetBybitRate("TONUSDT"); err == nil && rate != nil {
			usdValue = amount * rate.BestBid
		}
	} else if fromType == "crypto" {
		symbol := from + "USDT"
		if rate, err := apiCache.GetBybitRate(symbol); err == nil && rate != nil {
			usdValue = amount * rate.BestBid
		}
	}

	return usdValue >= minLargeOrderUSDT
}

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
		return 0, fmt.Errorf("failed to get rate")
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
