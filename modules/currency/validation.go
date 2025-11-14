package currency

import (
	"fmt"
	"math"
)

func isValidFloat(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func floatEquals(a, b float64) bool {
	if a == b {
		return true
	}

	diff := math.Abs(a - b)
	if a == 0 || b == 0 || diff < 1e-12 {
		return diff < 1e-9
	}

	absA := math.Abs(a)
	absB := math.Abs(b)
	largest := math.Max(absA, absB)
	return diff/largest < 1e-9
}

func floatGreaterOrEqual(a, b float64) bool {
	return a > b || floatEquals(a, b)
}

func ValidateAmount(amount float64) error {
	if amount <= 0 {
		return fmt.Errorf("amount must be positive")
	}
	if amount > maxConversionAmount {
		return fmt.Errorf("amount exceeds maximum limit")
	}
	if !isValidFloat(amount) {
		return fmt.Errorf("invalid amount")
	}
	return nil
}

func ValidateConversionResult(result float64, context string) error {
	if !isValidFloat(result) {
		return fmt.Errorf("%s: invalid result", context)
	}
	if result < minAmountAfterFees {
		return fmt.Errorf("%s: amount too small after fees", context)
	}
	return nil
}

func shouldUseOrderBookByUSD(usdValue float64) bool {
	return isValidFloat(usdValue) && usdValue >= minLargeOrderUSDT
}
