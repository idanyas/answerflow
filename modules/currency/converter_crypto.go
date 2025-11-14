package currency

import (
	"fmt"
)

func (m *CurrencyConverterModule) convertTONToUSDT(amount float64, apiCache *APICache) (float64, error) {
	rate, err := apiCache.GetBybitRate("TONUSDT")
	if err != nil {
		return 0, err
	}

	var gross float64
	usdValue := amount * rate.BestBid
	if shouldUseOrderBookByUSD(usdValue) {
		avgPrice, err := apiCache.GetBybitRateForAmount("TONUSDT", amount, false)
		if err != nil {
			return 0, fmt.Errorf("amount too large for current market liquidity")
		}
		gross = amount * avgPrice
	} else {
		if len(rate.OrderBookBids) > 0 && len(rate.OrderBookBids[0]) >= 2 {
			bidSize := rate.OrderBookBids[0][1]
			if bidSize < amount {
				avgPrice, err := apiCache.GetBybitRateForAmount("TONUSDT", amount, false)
				if err != nil {
					return 0, fmt.Errorf("insufficient liquidity for this amount")
				}
				gross = amount * avgPrice
			} else {
				gross = amount * rate.BestBid
			}
		} else {
			gross = amount * rate.BestBid
		}
	}

	result := gross * (1 - feeBybitTrade)
	if err := ValidateConversionResult(result, "TON->USDT"); err != nil {
		return 0, err
	}

	return result, nil
}

func (m *CurrencyConverterModule) convertUSDTToTON(usdt float64, apiCache *APICache) (float64, error) {
	var ton float64

	if shouldUseOrderBookByUSD(usdt) {
		t, _, err := apiCache.CalculateBuyAmountWithUSDT("TONUSDT", usdt)
		if err != nil {
			return 0, fmt.Errorf("amount too large for current market liquidity")
		}
		ton = t
	} else {
		rate, err := apiCache.GetBybitRate("TONUSDT")
		if err != nil {
			return 0, err
		}
		ton = usdt / rate.BestAsk
	}

	result := ton * (1 - feeBybitTrade)
	if err := ValidateConversionResult(result, "USDT->TON"); err != nil {
		return 0, err
	}

	return result, nil
}

func (m *CurrencyConverterModule) convertUSDTToCrypto(usdt float64, to string, apiCache *APICache) (float64, error) {
	symbol := to + "USDT"

	if err := apiCache.EnsureBybitSymbol(symbol); err != nil {
		return 0, fmt.Errorf("cryptocurrency %s not available: %w", to, err)
	}

	if !apiCache.IsTradeablePair(symbol) {
		return 0, fmt.Errorf("cryptocurrency %s not available for trading", to)
	}

	var crypto float64
	if shouldUseOrderBookByUSD(usdt) {
		c, _, err := apiCache.CalculateBuyAmountWithUSDT(symbol, usdt)
		if err != nil {
			return 0, fmt.Errorf("amount too large for current market liquidity")
		}
		crypto = c
	} else {
		rate, err := apiCache.GetBybitRate(symbol)
		if err != nil {
			return 0, err
		}
		crypto = usdt / rate.BestAsk
	}

	result := crypto * (1 - feeBybitTrade)
	if err := ValidateConversionResult(result, "USDT->"+to); err != nil {
		return 0, err
	}

	return result, nil
}

func (m *CurrencyConverterModule) convertCryptoToUSDT(amount float64, from string, apiCache *APICache) (float64, error) {
	symbol := from + "USDT"

	if err := apiCache.EnsureBybitSymbol(symbol); err != nil {
		return 0, fmt.Errorf("cryptocurrency %s not available: %w", from, err)
	}

	if !apiCache.IsTradeablePair(symbol) {
		return 0, fmt.Errorf("cryptocurrency %s not available for trading", from)
	}

	rate, err := apiCache.GetBybitRate(symbol)
	if err != nil {
		return 0, err
	}

	var gross float64
	usdValue := amount * rate.BestBid
	if shouldUseOrderBookByUSD(usdValue) {
		avgPrice, err := apiCache.GetBybitRateForAmount(symbol, amount, false)
		if err != nil {
			return 0, fmt.Errorf("amount too large for current market liquidity")
		}
		gross = amount * avgPrice
	} else {
		if len(rate.OrderBookBids) > 0 && len(rate.OrderBookBids[0]) >= 2 {
			bidSize := rate.OrderBookBids[0][1]
			if bidSize < amount {
				avgPrice, err := apiCache.GetBybitRateForAmount(symbol, amount, false)
				if err != nil {
					return 0, fmt.Errorf("insufficient liquidity for this amount")
				}
				gross = amount * avgPrice
			} else {
				gross = amount * rate.BestBid
			}
		} else {
			gross = amount * rate.BestBid
		}
	}

	result := gross * (1 - feeBybitTrade)
	if err := ValidateConversionResult(result, from+"->USDT"); err != nil {
		return 0, err
	}

	return result, nil
}

func (m *CurrencyConverterModule) convertCryptoPair(amount float64, from, to string, apiCache *APICache) (float64, error) {
	if from == CurrencyUSDT {
		return m.convertUSDTToCrypto(amount, to, apiCache)
	}
	if to == CurrencyUSDT {
		return m.convertCryptoToUSDT(amount, from, apiCache)
	}

	usdt, err := m.convertCryptoToUSDT(amount, from, apiCache)
	if err != nil {
		return 0, err
	}
	return m.convertUSDTToCrypto(usdt, to, apiCache)
}

func (m *CurrencyConverterModule) convertRUBToTON(amount float64, apiCache *APICache) (float64, error) {
	if !apiCache.IsWhitebirdAvailable() {
		return 0, fmt.Errorf("russian ruble exchange temporarily unavailable")
	}

	tonReceived, err := apiCache.GetWhitebirdRateForAmount(CurrencyRUB, CurrencyTON, amount)
	if err != nil {
		return 0, err
	}

	tonNet := tonReceived - feeTONWithdrawToBybit
	if tonNet <= 0 {
		return 0, fmt.Errorf("amount too small after withdrawal fee")
	}

	if err := ValidateConversionResult(tonNet, "RUB->TON"); err != nil {
		return 0, err
	}

	return tonNet, nil
}

func (m *CurrencyConverterModule) convertTONToRUB(amount float64, apiCache *APICache) (float64, error) {
	if !apiCache.IsWhitebirdAvailable() {
		return 0, fmt.Errorf("russian ruble exchange temporarily unavailable")
	}

	tonForWhitebird := amount - feeTONWithdrawToWhitebird
	if tonForWhitebird <= 0 {
		return 0, fmt.Errorf("amount too small after withdrawal fee (need at least 0.02 TON for fee)")
	}

	rubReceived, err := apiCache.GetWhitebirdRateForAmount(CurrencyTON, CurrencyRUB, tonForWhitebird)
	if err != nil {
		return 0, err
	}

	if err := ValidateConversionResult(rubReceived, "TON->RUB"); err != nil {
		return 0, err
	}

	return rubReceived, nil
}
