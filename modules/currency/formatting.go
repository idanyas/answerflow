package currency

import (
	"strconv"
	"strings"

	"github.com/leekchan/accounting"
)

var currencyDecimalPlaces = map[string]int{
	"BTC": 8, "WBTC": 8, "LBTC": 8,
	"ETH": 6, "TON": 6, "BNB": 6, "STETH": 6, "WETH": 6, "METH": 6,
	"SOL": 4, "AVAX": 4, "ATOM": 4, "NEAR": 4, "APT": 4, "SUI": 4,
	"DOGE": 4, "LTC": 4, "FIL": 4, "ICP": 4,
	"SHIB": 0, "PEPE": 0, "FLOKI": 0, "BONK": 0,
}

// GetCurrencyDecimalPlaces returns appropriate decimal places for currency
func GetCurrencyDecimalPlaces(currencyCode string) int {
	if decimals, ok := currencyDecimalPlaces[strings.ToUpper(currencyCode)]; ok {
		return decimals
	}
	return 2
}

// FormatAmount formats amount with thousand separators
func formatAmount(amount float64, currencyCode string) string {
	precision := GetCurrencyDecimalPlaces(currencyCode)
	ac := accounting.Accounting{
		Symbol:    "",
		Precision: precision,
		Thousand:  ",",
		Decimal:   ".",
	}
	return ac.FormatMoneyFloat64(amount)
}

// FormatAmountForClipboard formats for clipboard (no separators)
func formatAmountForClipboard(amount float64, currencyCode string) string {
	precision := GetCurrencyDecimalPlaces(currencyCode)

	if _, hasSpecific := currencyDecimalPlaces[strings.ToUpper(currencyCode)]; !hasSpecific {
		if amount < 0.01 {
			precision = 6
		} else if amount < 1 {
			precision = 4
		}
	}

	formatted := strconv.FormatFloat(amount, 'f', precision, 64)
	if strings.Contains(formatted, ".") {
		formatted = strings.TrimRight(formatted, "0")
		formatted = strings.TrimRight(formatted, ".")
	}
	return formatted
}

// FormatRate formats exchange rates with appropriate precision
func formatRate(rate float64) string {
	if !isValidFloat(rate) {
		return "N/A"
	}

	var formatted string
	switch {
	case rate < 0.00000001:
		formatted = strconv.FormatFloat(rate, 'e', 2, 64)
	case rate < 0.0001:
		formatted = strconv.FormatFloat(rate, 'f', 8, 64)
	case rate < 0.01:
		formatted = strconv.FormatFloat(rate, 'f', 6, 64)
	case rate < 1:
		formatted = strconv.FormatFloat(rate, 'f', 4, 64)
	case rate < 1000:
		formatted = strconv.FormatFloat(rate, 'f', 4, 64)
	case rate < 1000000:
		formatted = strconv.FormatFloat(rate, 'f', 2, 64)
	default:
		formatted = strconv.FormatFloat(rate, 'e', 2, 64)
	}

	if !strings.Contains(formatted, "e") && strings.Contains(formatted, ".") {
		formatted = strings.TrimRight(formatted, "0")
		formatted = strings.TrimRight(formatted, ".")
	}

	if formatted == "" || formatted == "." || formatted == "-" {
		return "N/A"
	}

	if strings.HasPrefix(formatted, ".") {
		formatted = "0" + formatted
	}
	if strings.HasPrefix(formatted, "-.") {
		formatted = "-0" + formatted[1:]
	}

	return formatted
}
