package calculator

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	"answerflow/commontypes"
	"answerflow/modules/currency" // For APICache type in ProcessQuery signature, even if unused by this module

	"github.com/expr-lang/expr"
)

const (
	calculatorScore = 75 // Score for a successful calculation result
	// Default icon path can be overridden by main.go or if iconPath in New is empty
	defaultCalculatorIconPath = "images/calculator_icon_internal.png"
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
		"sqrt":  func(x float64) float64 { return math.Sqrt(x) },
		"abs":   func(x float64) float64 { return math.Abs(x) },
		"log":   func(x float64) float64 { return math.Log(x) }, // Natural logarithm
		"log10": func(x float64) float64 { return math.Log10(x) },
		"log2":  func(x float64) float64 { return math.Log2(x) },
		"exp":   func(x float64) float64 { return math.Exp(x) }, // e^x
		"pow":   func(base, exponent float64) float64 { return math.Pow(base, exponent) },
		"sin":   func(x float64) float64 { return math.Sin(x) },
		"cos":   func(x float64) float64 { return math.Cos(x) },
		"tan":   func(x float64) float64 { return math.Tan(x) },
		"asin":  func(x float64) float64 { return math.Asin(x) },
		"acos":  func(x float64) float64 { return math.Acos(x) },
		"atan":  func(x float64) float64 { return math.Atan(x) },
		"atan2": func(y, x float64) float64 { return math.Atan2(y, x) },
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

// ProcessQuery handles a user's query for calculation.
// The apiCache parameter is part of the Module interface but not used by the Calculator.
func (m *CalculatorModule) ProcessQuery(ctx context.Context, query string, apiCache *currency.APICache) ([]commontypes.FlowResult, error) {
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return nil, nil
	}

	// Compile the expression.
	// expr.Env(m.mathEnv) makes the functions and constants available.
	program, err := expr.Compile(trimmedQuery, expr.Env(m.mathEnv))
	if err != nil {
		// Not a valid expression for the calculator or a syntax error.
		// This is common if the query is not a math expression.
		return nil, nil // No result, not a fatal error for the main app
	}

	// Run the compiled program.
	output, err := expr.Run(program, m.mathEnv)
	if err != nil {
		// Runtime error during evaluation (e.g., division by zero, log of negative, error from custom func like fact).
		// Log for debugging, but don't send error to user unless it's a specific "Error: message" result.
		log.Printf("Calculator: Error running expression '%s': %v", trimmedQuery, err)
		// Optionally, create a result that displays the error message to the user:
		// return []commontypes.FlowResult{{
		// 	Title:    fmt.Sprintf("Error: %s", err.Error()),
		// 	SubTitle: "Calculation failed",
		// 	IcoPath:  m.DefaultIconPath(),
		// 	Score:    calculatorScore - 10, // Lower score for error
		// }}, nil
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
		// If it's something else, this calculator module doesn't handle it.
		// log.Printf("Calculator: Unexpected result type for '%s': %T (%v)", trimmedQuery, output, output)
		return nil, nil
	}

	flowResult := commontypes.FlowResult{
		Title:    resultStr,
		SubTitle: fmt.Sprintf("Result for: %s", trimmedQuery),
		IcoPath:  m.DefaultIconPath(),
		Score:    calculatorScore,
		JsonRPCAction: commontypes.JsonRPCAction{
			Method:     "copy_to_clipboard",
			Parameters: []interface{}{resultStr},
		},
	}

	return []commontypes.FlowResult{flowResult}, nil
}
