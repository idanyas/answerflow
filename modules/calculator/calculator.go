package calculator

import (
	"context"
	"fmt"
	"log"
	"math"
	"regexp"
	"strconv"
	"strings"

	"answerflow/commontypes"
	"answerflow/modules/currency" // For APICache type in ProcessQuery signature, even if unused by this module

	"github.com/expr-lang/expr"
)

const (
	calculatorScore = 75 // Score for a successful calculation result
	// Default icon path using absolute URL for consistency
	defaultCalculatorIconPath = "https://img.icons8.com/badges/100/calculator.png"
)

// CalculatorModule handles mathematical expression queries.
type CalculatorModule struct {
	iconPath string
	mathEnv  map[string]interface{}
}

// NewCalculatorModule creates a new instance of the calculator module.
func NewCalculatorModule(iconPath string) *CalculatorModule {
	// Define math functions and constants for the expression environment.
	// Using explicit func wrappers for clarity and to ensure `expr` gets what it expects.
	mathFunctions := map[string]interface{}{
		"pi":    math.Pi,
		"e":     math.E,
		"phi":   (1 + math.Sqrt(5)) / 2, // Golden ratio
		"sqrt":  func(x float64) float64 { return math.Sqrt(x) },
		"cbrt":  func(x float64) float64 { return math.Cbrt(x) }, // Cube root
		"abs":   func(x float64) float64 { return math.Abs(x) },
		"log":   func(x float64) float64 { return math.Log(x) }, // Natural logarithm
		"log10": func(x float64) float64 { return math.Log10(x) },
		"log2":  func(x float64) float64 { return math.Log2(x) },
		"logb":  func(x, base float64) float64 { return math.Log(x) / math.Log(base) }, // Log with custom base
		"exp":   func(x float64) float64 { return math.Exp(x) },                        // e^x
		"pow":   func(base, exponent float64) float64 { return math.Pow(base, exponent) },
		"sin":   func(x float64) float64 { return math.Sin(x) },
		"cos":   func(x float64) float64 { return math.Cos(x) },
		"tan":   func(x float64) float64 { return math.Tan(x) },
		"asin":  func(x float64) float64 { return math.Asin(x) },
		"acos":  func(x float64) float64 { return math.Acos(x) },
		"atan":  func(x float64) float64 { return math.Atan(x) },
		"atan2": func(y, x float64) float64 { return math.Atan2(y, x) },
		"sind":  func(deg float64) float64 { return math.Sin(deg * math.Pi / 180) }, // Sin with degrees
		"cosd":  func(deg float64) float64 { return math.Cos(deg * math.Pi / 180) }, // Cos with degrees
		"tand":  func(deg float64) float64 { return math.Tan(deg * math.Pi / 180) }, // Tan with degrees
		"asind": func(x float64) float64 { return math.Asin(x) * 180 / math.Pi },    // Asin returning degrees
		"acosd": func(x float64) float64 { return math.Acos(x) * 180 / math.Pi },    // Acos returning degrees
		"atand": func(x float64) float64 { return math.Atan(x) * 180 / math.Pi },    // Atan returning degrees
		"ceil":  func(x float64) float64 { return math.Ceil(x) },
		"floor": func(x float64) float64 { return math.Floor(x) },
		"round": func(x float64) float64 { return math.Round(x) },     // Go's Round rounds to nearest even for x.5
		"min":   func(x, y float64) float64 { return math.Min(x, y) }, // Can take two args
		"max":   func(x, y float64) float64 { return math.Max(x, y) }, // Can take two args
		"mod":   func(x, y float64) float64 { return math.Mod(x, y) },
		// Factorial example (integer based)
		"fact": func(n int) (int, error) {
			if n < 0 {
				return 0, fmt.Errorf("factorial is not defined for negative numbers")
			}
			if n > 20 { // Prevent overflow for int, and very long calculations
				return 0, fmt.Errorf("factorial input %d too large (max 20)", n)
			}
			res := 1
			for i := 2; i <= n; i++ {
				res *= i
			}
			return res, nil
		},
	}

	effectiveIconPath := iconPath
	if effectiveIconPath == "" {
		effectiveIconPath = defaultCalculatorIconPath
	}

	return &CalculatorModule{
		iconPath: effectiveIconPath,
		mathEnv:  mathFunctions,
	}
}

// Name returns the name of the module.
func (m *CalculatorModule) Name() string {
	return "Calculator"
}

// DefaultIconPath returns the default icon for this module.
func (m *CalculatorModule) DefaultIconPath() string {
	return m.iconPath
}

// Regex to find numbers that may have spaces, commas, or dots as separators.
// Now includes the literal non-breaking space character.
var numberRegex = regexp.MustCompile(`[0-9]+(?:[0-9\s ,.]*[0-9])?`)

// normalizeNumberString cleans up a string that represents a number by removing thousand separators
// and standardizing the decimal separator. It handles standard and non-breaking spaces.
func normalizeNumberString(s string) string {
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, " ", "") // Handle non-breaking spaces

	dotIdx := strings.LastIndex(s, ".")
	commaIdx := strings.LastIndex(s, ",")

	if dotIdx != -1 && commaIdx != -1 {
		if commaIdx > dotIdx { // Comma is decimal separator, dot is thousand separator
			s = strings.ReplaceAll(s, ".", "")
			s = strings.Replace(s, ",", ".", 1)
		} else { // Dot is decimal separator, comma is thousand separator
			s = strings.ReplaceAll(s, ",", "")
		}
	} else if commaIdx != -1 { // Only commas present
		parts := strings.Split(s, ",")
		if len(parts) > 1 {
			lastPart := parts[len(parts)-1]
			// Heuristic: if last part after a comma is 1-3 digits, assume comma was decimal.
			if len(lastPart) >= 1 && len(lastPart) <= 3 && regexp.MustCompile(`^\d+$`).MatchString(lastPart) {
				if strings.Count(s, ",") == 1 { // Only one comma, likely decimal
					firstPart := strings.Join(parts[:len(parts)-1], "")
					s = firstPart + "." + lastPart
				} else { // Multiple commas, likely thousand separators
					s = strings.ReplaceAll(s, ",", "")
				}
			} else { // Comma likely a thousand separator
				s = strings.ReplaceAll(s, ",", "")
			}
		}
	}
	return s
}

func preprocessQuery(query string) string {
	// 1. Handle percentages: replace '%' with '/100.0'.
	// This handles simple cases like "10%" or "5*20%".
	// It does NOT handle "50+10%" as "50 + (50 * 0.1)". It calculates it as 50 + 0.1.
	processedQuery := strings.ReplaceAll(query, "%", "/100.0")

	// 2. Normalize number formats within the expression.
	processedQuery = numberRegex.ReplaceAllStringFunc(processedQuery, func(match string) string {
		return normalizeNumberString(match)
	})

	return processedQuery
}

// ProcessQuery handles a user's query for calculation.
// The apiCache parameter is part of the Module interface but not used by the Calculator.
func (m *CalculatorModule) ProcessQuery(ctx context.Context, query string, apiCache *currency.APICache) ([]commontypes.FlowResult, error) {
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return nil, nil
	}

	// Preprocess the query to handle non-standard number formats and percentages.
	processedQuery := preprocessQuery(trimmedQuery)

	// Compile the expression.
	// expr.Env(m.mathEnv) makes the functions and constants available.
	program, err := expr.Compile(processedQuery, expr.Env(m.mathEnv))
	if err != nil {
		// Not a valid expression for the calculator or a syntax error.
		// This is common if the query is not a math expression.
		return nil, nil // No result, not a fatal error for the main app
	}

	// Run the compiled program.
	output, err := expr.Run(program, m.mathEnv)
	if err != nil {
		// Runtime error during evaluation (e.g., division by zero, log of negative, error from custom func like fact).
		log.Printf("Calculator: Error running expression '%s' (processed from '%s'): %v", processedQuery, trimmedQuery, err)
		return nil, nil // For now, only successful results
	}

	// Format the output.
	var resultStr string
	switch v := output.(type) {
	case float64:
		// Format with up to 8 decimal places, then trim trailing zeros and decimal point if it's a whole number.
		resultStr = strconv.FormatFloat(v, 'f', 8, 64)
		resultStr = strings.TrimRight(resultStr, "0")
		resultStr = strings.TrimRight(resultStr, ".")
	case int:
		resultStr = strconv.Itoa(v)
	case int64: // expr might use int64 for some integer operations
		resultStr = strconv.FormatInt(v, 10)
	case bool:
		resultStr = strconv.FormatBool(v) // For expressions like "5 > 3"
	default:
		return nil, nil
	}

	subtitle := fmt.Sprintf("Result for: %s", trimmedQuery)

	flowResult := commontypes.FlowResult{
		Title:    resultStr,
		SubTitle: subtitle,
		IcoPath:  m.DefaultIconPath(),
		Score:    calculatorScore,
		JsonRPCAction: commontypes.JsonRPCAction{
			Method:     "copy_to_clipboard",
			Parameters: []interface{}{resultStr},
		},
	}

	return []commontypes.FlowResult{flowResult}, nil
}
