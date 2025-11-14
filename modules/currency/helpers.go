package currency

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/leekchan/accounting"
)

// ============================================================================
// Float Validation & Comparison
// ============================================================================

func isValidFloat(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func floatEquals(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func floatGreaterOrEqual(a, b float64) bool {
	return a > b || floatEquals(a, b)
}

// ============================================================================
// Amount Validation
// ============================================================================

func ValidateAmount(amount float64) error {
	if amount <= 0 {
		return fmt.Errorf("amount must be positive")
	}
	if amount > maxConversionAmount {
		return fmt.Errorf("amount too large")
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return fmt.Errorf("invalid amount")
	}
	return nil
}

func ValidateConversionResult(result float64, context string) error {
	if !isValidFloat(result) {
		return fmt.Errorf("%s: invalid result", context)
	}
	if result < minAmountAfterFees {
		return fmt.Errorf("%s: amount too small", context)
	}
	return nil
}

// ValidateWhitebirdRates checks if Whitebird rates are within acceptable ranges
func (ac *APICache) ValidateWhitebirdRates() bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	rubToTon, ok1 := ac.whitebirdRates["RUB_TON_BUY"]
	tonToRub, ok2 := ac.whitebirdRates["TON_RUB_SELL"]

	if !ok1 || !ok2 || !isValidFloat(rubToTon) || !isValidFloat(tonToRub) {
		return false
	}

	if rubToTon < whitebirdRateMin || rubToTon > whitebirdRateMax {
		return false
	}
	if tonToRub < whitebirdRateMin || tonToRub > whitebirdRateMax {
		return false
	}

	spread := (rubToTon - tonToRub) / rubToTon
	return spread > whitebirdMinSpread && spread < whitebirdMaxSpread
}

// ============================================================================
// Retry Helper
// ============================================================================

func retryWithBackoff(fn func() error) error {
	var lastErr error
	delay := baseRetryDelay

	for i := 0; i < maxRetries; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
			if i < maxRetries-1 {
				time.Sleep(delay)
				delay *= 2
			}
		}
	}
	return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// ============================================================================
// Currency Type Detection
// ============================================================================

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

// ============================================================================
// Decimal Places Configuration
// ============================================================================

var currencyDecimalPlaces = map[string]int{
	"BTC": 8, "WBTC": 8, "LBTC": 8,
	"ETH": 6, "TON": 6, "BNB": 6, "STETH": 6, "WETH": 6, "METH": 6,
	"SOL": 4, "AVAX": 4, "ATOM": 4, "NEAR": 4, "APT": 4, "SUI": 4,
	"DOGE": 4, "LTC": 4, "FIL": 4, "ICP": 4,
	"SHIB": 0, "PEPE": 0, "FLOKI": 0, "BONK": 0,
}

func GetCurrencyDecimalPlaces(currencyCode string) int {
	if decimals, ok := currencyDecimalPlaces[currencyCode]; ok {
		return decimals
	}
	return 2
}

// ============================================================================
// Formatting Functions
// ============================================================================

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

func formatAmountForClipboard(amount float64, currencyCode string) string {
	precision := GetCurrencyDecimalPlaces(currencyCode)

	if _, hasSpecific := currencyDecimalPlaces[currencyCode]; !hasSpecific {
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

func formatRate(rate float64) string {
	if !isValidFloat(rate) {
		return "N/A"
	}

	var formatted string
	switch {
	case rate < 0.0001:
		formatted = strconv.FormatFloat(rate, 'f', 8, 64)
	case rate < 1:
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

	return formatted
}
