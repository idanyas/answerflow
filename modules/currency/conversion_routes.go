package currency

import (
	"fmt"
)

func (m *CurrencyConverterModule) routeConversion(amount float64, from, to string, apiCache *APICache) (float64, error) {
	fromType := getCurrencyType(from, apiCache)
	toType := getCurrencyType(to, apiCache)

	switch {
	case fromType == "RUB" && toType == "TON":
		return m.convertRUBToTON(amount, apiCache)
	case fromType == "TON" && toType == "RUB":
		return m.convertTONToRUB(amount, apiCache)
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
		return 0, fmt.Errorf("unsupported conversion")
	}
}

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
			return 0, err
		}
		currentCurrency = intermediate
	}

	if currentCurrency != to {
		return m.convertDirectPair(current, currentCurrency, to, apiCache)
	}

	return current, nil
}

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
		return 0, fmt.Errorf("unsupported pair")
	}
}
