package currency

import (
	"fmt"
	"time"
)

func (ac *APICache) IsTradeablePair(symbol string) bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	if time.Since(ac.pairsLastCheck) > time.Hour {
		go ac.refreshTradeablePairs()
	}

	return ac.tradeablePairs[symbol]
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
		return 0, fmt.Errorf("unsupported Whitebird pair: %s -> %s", from, to)
	}

	rate, ok := ac.whitebirdRates[key]
	if !ok || !isValidFloat(rate) {
		return 0, fmt.Errorf("rate not available for %s", key)
	}
	return rate, nil
}

func (ac *APICache) GetBybitRate(symbol string) (*BybitRate, error) {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	rate, ok := ac.bybitRates[symbol]
	if !ok || rate == nil || !isValidFloat(rate.BestBid) || !isValidFloat(rate.BestAsk) {
		return nil, fmt.Errorf("rate not available for %s", symbol)
	}

	return &BybitRate{
		BestBid:       rate.BestBid,
		BestAsk:       rate.BestAsk,
		OrderBookBids: rate.OrderBookBids,
		OrderBookAsks: rate.OrderBookAsks,
		LastUpdate:    rate.LastUpdate,
		Volume24h:     rate.Volume24h,
	}, nil
}

func (ac *APICache) GetBybitRateForAmount(symbol string, amount float64, isBuy bool) (float64, error) {
	avgPrice, err := ac.CalculateAverageExecutionPrice(symbol, amount, isBuy)
	if err != nil {
		rate, err := ac.GetBybitRate(symbol)
		if err != nil {
			return 0, fmt.Errorf("rate not available for %s: %w", symbol, err)
		}

		if isBuy {
			if !isValidFloat(rate.BestAsk) {
				return 0, fmt.Errorf("invalid ask rate for %s", symbol)
			}
			return rate.BestAsk, nil
		}
		if !isValidFloat(rate.BestBid) {
			return 0, fmt.Errorf("invalid bid rate for %s", symbol)
		}
		return rate.BestBid, nil
	}

	return avgPrice, nil
}

func (ac *APICache) GetMastercardRate(from, to string) (float64, error) {
	if from == to {
		return 1.0, nil
	}

	if from == "USD" {
		key := fmt.Sprintf("%s_%s", from, to)
		ac.mu.RLock()
		defer ac.mu.RUnlock()

		rate, ok := ac.mastercardRates[key]
		if !ok || !isValidFloat(rate) {
			return 0, fmt.Errorf("rate not available for %s", key)
		}
		return rate, nil
	}

	if to == "USD" {
		key := fmt.Sprintf("USD_%s", from)
		ac.mu.RLock()
		defer ac.mu.RUnlock()

		rate, ok := ac.mastercardRates[key]
		if !ok || !isValidFloat(rate) {
			return 0, fmt.Errorf("rate not available for %s", key)
		}
		return 1.0 / rate, nil
	}

	ac.mu.RLock()
	defer ac.mu.RUnlock()

	fromKey := fmt.Sprintf("USD_%s", from)
	toKey := fmt.Sprintf("USD_%s", to)

	fromRate, okFrom := ac.mastercardRates[fromKey]
	toRate, okTo := ac.mastercardRates[toKey]

	if !okFrom || !okTo || !isValidFloat(fromRate) || !isValidFloat(toRate) {
		return 0, fmt.Errorf("cross rate not available for %s to %s", from, to)
	}

	return toRate / fromRate, nil
}
