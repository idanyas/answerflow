package currency

import (
	"fmt"
)

// routeConversion decides actual path and executes it.
func (m *CurrencyConverterModule) routeConversion(amount float64, from, to string, apiCache *APICache) (float64, error) {
	fromType := getCurrencyType(from, apiCache)
	toType := getCurrencyType(to, apiCache)

	// Direct RUB ↔ TON conversions
	if fromType == "RUB" && toType == "TON" {
		return m.convertRUBToTON(amount, apiCache)
	}
	if fromType == "TON" && toType == "RUB" {
		return m.convertTONToRUB(amount, apiCache)
	}

	// RUB to other currencies via TON bridge
	if fromType == "RUB" && toType == "crypto" {
		return m.convertViaRoute(amount, from, to, apiCache, []string{"TON", "USDT"})
	}
	if fromType == "RUB" && toType == "fiat" {
		return m.convertViaRoute(amount, from, to, apiCache, []string{"TON", "USDT", "USD"})
	}

	// Other currencies to RUB via TON bridge
	if fromType == "crypto" && toType == "RUB" {
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USDT", "TON"})
	}
	if fromType == "fiat" && toType == "RUB" {
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USD", "USDT", "TON"})
	}

	// Crypto ↔ Crypto via USDT
	if fromType == "crypto" && toType == "crypto" {
		return m.convertCryptoPair(amount, from, to, apiCache)
	}

	// Fiat ↔ Fiat via USD/Mastercard
	if fromType == "fiat" && toType == "fiat" {
		return m.convertFiatPair(amount, from, to, apiCache)
	}

	// TON ↔ Crypto via USDT
	if fromType == "TON" && toType == "crypto" {
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USDT"})
	}
	if fromType == "crypto" && toType == "TON" {
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USDT"})
	}

	// TON ↔ Fiat via USDT and USD
	if fromType == "TON" && toType == "fiat" {
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USDT", "USD"})
	}
	if fromType == "fiat" && toType == "TON" {
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USD", "USDT"})
	}

	// Crypto ↔ Fiat (non-USD) via USDT and USD
	if fromType == "crypto" && toType == "fiat" && to != "USD" {
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USDT", "USD"})
	}
	if fromType == "fiat" && toType == "crypto" && from != "USD" {
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USD", "USDT"})
	}

	// Crypto ↔ USD (direct via USDT)
	if fromType == "crypto" && to == "USD" {
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USDT"})
	}
	if from == "USD" && toType == "crypto" {
		return m.convertViaRoute(amount, from, to, apiCache, []string{"USDT"})
	}

	return 0, fmt.Errorf("conversion route not available")
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

	// RUB ↔ TON direct conversions (CRITICAL FIX #2)
	if from == "RUB" && to == "TON" {
		return m.convertRUBToTON(amount, apiCache)
	}
	if from == "TON" && to == "RUB" {
		return m.convertTONToRUB(amount, apiCache)
	}

	// TON ↔ USDT conversions
	if from == "TON" && to == "USDT" {
		return m.convertTONToUSDT(amount, apiCache)
	}
	if from == "USDT" && to == "TON" {
		return m.convertUSDTToTON(amount, apiCache)
	}

	// USDT ↔ USD conversions (Bybit Card fee)
	if from == "USDT" && to == "USD" {
		return amount * (1 - feeUSDTToUSD), nil
	}
	if from == "USD" && to == "USDT" {
		return amount * (1 - feeUSDToUSDT), nil
	}

	// Crypto ↔ USDT conversions
	if fromType == "crypto" && to == "USDT" {
		return m.convertCryptoToUSDT(amount, from, apiCache)
	}
	if from == "USDT" && toType == "crypto" {
		return m.convertUSDTToCrypto(amount, to, apiCache)
	}

	// Fiat ↔ USD conversions (Mastercard)
	if fromType == "fiat" && to == "USD" {
		return m.convertFiatToUSD(amount, from, apiCache)
	}
	if from == "USD" && toType == "fiat" {
		return m.convertUSDToFiat(amount, to, apiCache)
	}

	return 0, fmt.Errorf("conversion not available")
}

// planRoute returns the sequence of currency "legs" used by the router, for fee display.
func (m *CurrencyConverterModule) planRoute(from, to string, apiCache *APICache) []string {
	fromType := getCurrencyType(from, apiCache)
	toType := getCurrencyType(to, apiCache)

	legs := []string{from}
	appendLegs := func(more ...string) {
		for _, x := range more {
			if legs[len(legs)-1] != x {
				legs = append(legs, x)
			}
		}
	}

	switch {
	case fromType == "RUB" && toType == "TON":
		appendLegs("TON")
	case fromType == "TON" && toType == "RUB":
		appendLegs("RUB")
	case fromType == "RUB" && toType == "crypto":
		appendLegs("TON", "USDT", to)
	case fromType == "RUB" && toType == "fiat":
		appendLegs("TON", "USDT", "USD", to)
	case fromType == "crypto" && toType == "RUB":
		appendLegs("USDT", "TON", "RUB")
	case fromType == "fiat" && toType == "RUB":
		appendLegs("USD", "USDT", "TON", "RUB")
	case fromType == "crypto" && toType == "crypto":
		// via USDT
		if from != "USDT" {
			appendLegs("USDT")
		}
		if to != "USDT" {
			appendLegs(to)
		}
	case fromType == "fiat" && toType == "fiat":
		// via USD with Mastercard
		if from != "USD" {
			appendLegs("USD")
		}
		if to != "USD" {
			appendLegs(to)
		}
	case fromType == "TON" && toType == "crypto":
		appendLegs("USDT", to)
	case fromType == "crypto" && toType == "TON":
		appendLegs("USDT", "TON")
	case fromType == "TON" && toType == "fiat":
		appendLegs("USDT", "USD", to)
	case fromType == "fiat" && toType == "TON":
		appendLegs("USD", "USDT", "TON")
	case fromType == "crypto" && toType == "fiat":
		appendLegs("USDT", "USD", to)
	case fromType == "fiat" && toType == "crypto":
		appendLegs("USD", "USDT", to)
	default:
		// unknown path
	}
	return legs
}
