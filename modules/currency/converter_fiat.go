package currency

import (
	"fmt"
)

func (m *CurrencyConverterModule) convertFiatToUSD(amount float64, from string, apiCache *APICache) (float64, error) {
	if from == CurrencyUSD {
		return amount, nil
	}

	rate, err := apiCache.GetMastercardRate(from, CurrencyUSD)
	if err != nil {
		return 0, err
	}

	result := amount * rate / (1 + feeMastercard)
	if err := ValidateConversionResult(result, "fiat->USD"); err != nil {
		return 0, err
	}

	return result, nil
}

func (m *CurrencyConverterModule) convertUSDToFiat(amount float64, to string, apiCache *APICache) (float64, error) {
	if to == CurrencyUSD {
		return amount, nil
	}

	rate, err := apiCache.GetMastercardRate(CurrencyUSD, to)
	if err != nil {
		return 0, err
	}

	result := amount * rate / (1 + feeMastercard)
	if err := ValidateConversionResult(result, "USD->fiat"); err != nil {
		return 0, err
	}

	return result, nil
}

func (m *CurrencyConverterModule) convertFiatPair(amount float64, from, to string, apiCache *APICache) (float64, error) {
	if from == to {
		return amount, nil
	}

	if !apiCache.IsMastercardAvailable() {
		return 0, fmt.Errorf("fiat currency exchange temporarily unavailable")
	}

	usd, err := m.convertFiatToUSD(amount, from, apiCache)
	if err != nil {
		return 0, err
	}

	if to == CurrencyUSD {
		return usd, nil
	}
	return m.convertUSDToFiat(usd, to, apiCache)
}
