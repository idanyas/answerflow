package currency

import (
	"fmt"
	"log"
)

// convertCryptoPair handles crypto-to-crypto conversions via USDT
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

// convertFiatPair handles fiat-to-fiat conversions via USD
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

// convertFiatToUSD converts fiat to USD via Mastercard
func (m *CurrencyConverterModule) convertFiatToUSD(amount float64, from string, apiCache *APICache) (float64, error) {
	if from == "USD" {
		return amount, nil
	}

	rate, err := apiCache.GetMastercardRate(from, "USD")
	if err != nil {
		return 0, fmt.Errorf("mastercard %s->USD rate: %w", from, err)
	}

	result := amount * rate * (1 - feeMastercard)
	return result, ValidateConversionResult(result, "fiat->USD")
}

// convertUSDToFiat converts USD to fiat via Mastercard
func (m *CurrencyConverterModule) convertUSDToFiat(amount float64, to string, apiCache *APICache) (float64, error) {
	if to == "USD" {
		return amount, nil
	}

	rate, err := apiCache.GetMastercardRate("USD", to)
	if err != nil {
		return 0, fmt.Errorf("mastercard USD->%s rate: %w", to, err)
	}

	result := amount * rate * (1 - feeMastercard)
	return result, ValidateConversionResult(result, "USD->fiat")
}

// convertRUBToTONDirect converts RUB to TON via Whitebird
func (m *CurrencyConverterModule) convertRUBToTONDirect(amount float64, apiCache *APICache) (float64, error) {
	rate, err := apiCache.GetWhitebirdRate("RUB", "TON")
	if err != nil {
		return 0, fmt.Errorf("whitebird RUB->TON rate: %w", err)
	}

	log.Printf("RUB->TON: amount=%f RUB, rate=%f RUB/TON", amount, rate)

	ton := amount / rate
	ton -= feeTONWithdrawToBybit

	return ton, ValidateConversionResult(ton, "RUB->TON")
}

// convertTONToRUBDirect converts TON to RUB via Whitebird
func (m *CurrencyConverterModule) convertTONToRUBDirect(amount float64, apiCache *APICache) (float64, error) {
	ton := amount - feeTONWithdrawToWhitebird
	if ton <= 0 {
		return 0, fmt.Errorf("amount too small after withdrawal fee")
	}

	rate, err := apiCache.GetWhitebirdRate("TON", "RUB")
	if err != nil {
		return 0, fmt.Errorf("whitebird TON->RUB rate: %w", err)
	}

	log.Printf("TON->RUB: amount=%f TON (after fee), rate=%f RUB/TON", ton, rate)

	result := ton * rate
	return result, ValidateConversionResult(result, "TON->RUB")
}

// convertTONToUSDT sells TON for USDT on Bybit
func (m *CurrencyConverterModule) convertTONToUSDT(amount float64, apiCache *APICache) (float64, error) {
	rate, err := apiCache.GetBybitRate("TONUSDT")
	if err != nil {
		return 0, fmt.Errorf("bybit TONUSDT rate: %w", err)
	}

	var grossUSDT float64
	if shouldUseOrderBook(amount, "TON", "USDT", apiCache) {
		avgPrice, err := apiCache.GetBybitRateForAmount("TONUSDT", amount, false)
		if err != nil {
			log.Printf("Orderbook failed for %.2f TON, using bid: %v", amount, err)
			grossUSDT = amount * rate.BestBid
		} else {
			grossUSDT = amount * avgPrice
		}
	} else {
		grossUSDT = amount * rate.BestBid
	}

	result := grossUSDT * (1 - feeBybitTrade)
	return result, ValidateConversionResult(result, "TON->USDT")
}

// convertUSDTToTON buys TON with USDT on Bybit
func (m *CurrencyConverterModule) convertUSDTToTON(usdt float64, apiCache *APICache) (float64, error) {
	var ton float64
	var err error

	if shouldUseOrderBook(usdt, "USDT", "TON", apiCache) {
		ton, _, err = apiCache.CalculateBuyAmountWithUSDT("TONUSDT", usdt)
		if err != nil {
			rate, err := apiCache.GetBybitRate("TONUSDT")
			if err != nil {
				return 0, fmt.Errorf("bybit TONUSDT rate: %w", err)
			}
			ton = usdt / rate.BestAsk
		}
	} else {
		rate, err := apiCache.GetBybitRate("TONUSDT")
		if err != nil {
			return 0, fmt.Errorf("bybit TONUSDT rate: %w", err)
		}
		ton = usdt / rate.BestAsk
	}

	result := ton * (1 - feeBybitTrade)
	return result, ValidateConversionResult(result, "USDT->TON")
}

// convertUSDTToCrypto buys crypto with USDT on Bybit
func (m *CurrencyConverterModule) convertUSDTToCrypto(usdt float64, toCrypto string, apiCache *APICache) (float64, error) {
	symbol := toCrypto + "USDT"

	if !apiCache.IsTradeablePair(symbol) {
		return 0, fmt.Errorf("%s not tradeable on Bybit", symbol)
	}

	var crypto float64
	if shouldUseOrderBook(usdt, "USDT", toCrypto, apiCache) {
		var err error
		crypto, _, err = apiCache.CalculateBuyAmountWithUSDT(symbol, usdt)
		if err != nil {
			rate, err := apiCache.GetBybitRate(symbol)
			if err != nil {
				return 0, fmt.Errorf("bybit %s rate: %w", symbol, err)
			}
			crypto = usdt / rate.BestAsk
		}
	} else {
		rate, err := apiCache.GetBybitRate(symbol)
		if err != nil {
			return 0, fmt.Errorf("bybit %s rate: %w", symbol, err)
		}
		crypto = usdt / rate.BestAsk
	}

	result := crypto * (1 - feeBybitTrade)
	return result, ValidateConversionResult(result, "USDT->"+toCrypto)
}

// convertCryptoToUSDT sells crypto for USDT on Bybit
func (m *CurrencyConverterModule) convertCryptoToUSDT(amount float64, fromCrypto string, apiCache *APICache) (float64, error) {
	symbol := fromCrypto + "USDT"

	if !apiCache.IsTradeablePair(symbol) {
		return 0, fmt.Errorf("%s not tradeable on Bybit", symbol)
	}

	rate, err := apiCache.GetBybitRate(symbol)
	if err != nil {
		return 0, fmt.Errorf("bybit %s rate: %w", symbol, err)
	}

	var grossUSDT float64
	if shouldUseOrderBook(amount*rate.BestBid, "CRYPTO", "USDT", apiCache) {
		avgPrice, err := apiCache.GetBybitRateForAmount(symbol, amount, false)
		if err != nil {
			log.Printf("Orderbook failed for %.8f %s, using bid: %v", amount, fromCrypto, err)
			grossUSDT = amount * rate.BestBid
		} else {
			grossUSDT = amount * avgPrice
		}
	} else {
		grossUSDT = amount * rate.BestBid
	}

	result := grossUSDT * (1 - feeBybitTrade)
	return result, ValidateConversionResult(result, fromCrypto+"->USDT")
}

// shouldUseOrderBook determines if order book calculation is needed
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
