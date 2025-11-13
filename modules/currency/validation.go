package currency

import (
	"fmt"
	"math"
)

const (
	minAmountAfterFees  = 0.000001
	maxConversionAmount = 1e15
	floatEpsilon        = 1e-9
)

// ValidateAmount performs comprehensive amount validation
func ValidateAmount(amount float64) error {
	if amount <= 0 {
		return fmt.Errorf("amount must be positive: %f", amount)
	}
	if amount > maxConversionAmount {
		return fmt.Errorf("amount too large: %f", amount)
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return fmt.Errorf("amount is NaN or Inf: %f", amount)
	}
	return nil
}

// IsValidFloat checks if a float is positive, finite, and not NaN
func isValidFloat(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// FloatEquals compares two floats with epsilon tolerance
func floatEquals(a, b float64) bool {
	return math.Abs(a-b) < floatEpsilon
}

// FloatGreaterOrEqual checks if a >= b with epsilon tolerance
func floatGreaterOrEqual(a, b float64) bool {
	return a > b || floatEquals(a, b)
}

// ValidateConversionResult validates final conversion result
func ValidateConversionResult(result float64, context string) error {
	if !isValidFloat(result) {
		return fmt.Errorf("%s: invalid result %f", context, result)
	}
	if result < minAmountAfterFees {
		return fmt.Errorf("%s: amount too small after fees %f", context, result)
	}
	return nil
}
