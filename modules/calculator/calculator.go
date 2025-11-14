package calculator

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"answerflow/commontypes"
	"answerflow/modules/currency"

	"github.com/expr-lang/expr"
)

const calculatorScore = 75

type CalculatorModule struct {
	iconPath string
	mathEnv  map[string]interface{}
}

func NewCalculatorModule(iconPath string) *CalculatorModule {
	mathEnv := map[string]interface{}{
		"pi":    math.Pi,
		"e":     math.E,
		"phi":   (1 + math.Sqrt(5)) / 2,
		"sqrt":  func(x float64) float64 { return math.Sqrt(x) },
		"cbrt":  func(x float64) float64 { return math.Cbrt(x) },
		"abs":   func(x float64) float64 { return math.Abs(x) },
		"log":   func(x float64) float64 { return math.Log(x) },
		"log10": func(x float64) float64 { return math.Log10(x) },
		"log2":  func(x float64) float64 { return math.Log2(x) },
		"logb":  func(x, base float64) float64 { return math.Log(x) / math.Log(base) },
		"exp":   func(x float64) float64 { return math.Exp(x) },
		"pow":   func(base, exp float64) float64 { return math.Pow(base, exp) },
		"sin":   func(x float64) float64 { return math.Sin(x) },
		"cos":   func(x float64) float64 { return math.Cos(x) },
		"tan":   func(x float64) float64 { return math.Tan(x) },
		"asin":  func(x float64) float64 { return math.Asin(x) },
		"acos":  func(x float64) float64 { return math.Acos(x) },
		"atan":  func(x float64) float64 { return math.Atan(x) },
		"atan2": func(y, x float64) float64 { return math.Atan2(y, x) },
		"sind":  func(deg float64) float64 { return math.Sin(deg * math.Pi / 180) },
		"cosd":  func(deg float64) float64 { return math.Cos(deg * math.Pi / 180) },
		"tand":  func(deg float64) float64 { return math.Tan(deg * math.Pi / 180) },
		"asind": func(x float64) float64 { return math.Asin(x) * 180 / math.Pi },
		"acosd": func(x float64) float64 { return math.Acos(x) * 180 / math.Pi },
		"atand": func(x float64) float64 { return math.Atan(x) * 180 / math.Pi },
		"ceil":  func(x float64) float64 { return math.Ceil(x) },
		"floor": func(x float64) float64 { return math.Floor(x) },
		"round": func(x float64) float64 { return math.Round(x) },
		"min":   func(x, y float64) float64 { return math.Min(x, y) },
		"max":   func(x, y float64) float64 { return math.Max(x, y) },
		"mod":   func(x, y float64) float64 { return math.Mod(x, y) },
		"fact": func(n int) (int, error) {
			if n < 0 {
				return 0, fmt.Errorf("factorial undefined for negative")
			}
			if n > 20 {
				return 0, fmt.Errorf("factorial too large")
			}
			res := 1
			for i := 2; i <= n; i++ {
				res *= i
			}
			return res, nil
		},
	}

	if iconPath == "" {
		iconPath = "https://img.icons8.com/badges/100/calculator.png"
	}

	return &CalculatorModule{
		iconPath: iconPath,
		mathEnv:  mathEnv,
	}
}

func (m *CalculatorModule) Name() string {
	return "Calculator"
}

func (m *CalculatorModule) DefaultIconPath() string {
	return m.iconPath
}

var numberRegex = regexp.MustCompile(`[0-9]+(?:[0-9\s ,.]*[0-9])?`)

func normalizeNumberString(s string) string {
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, " ", "")

	dotIdx := strings.LastIndex(s, ".")
	commaIdx := strings.LastIndex(s, ",")

	if dotIdx != -1 && commaIdx != -1 {
		if commaIdx > dotIdx {
			s = strings.ReplaceAll(s, ".", "")
			s = strings.Replace(s, ",", ".", 1)
		} else {
			s = strings.ReplaceAll(s, ",", "")
		}
	} else if commaIdx != -1 {
		parts := strings.Split(s, ",")
		if len(parts) > 1 {
			lastPart := parts[len(parts)-1]
			if len(lastPart) >= 1 && len(lastPart) <= 3 && regexp.MustCompile(`^\d+$`).MatchString(lastPart) {
				if strings.Count(s, ",") == 1 {
					s = strings.Join(parts[:len(parts)-1], "") + "." + lastPart
				} else {
					s = strings.ReplaceAll(s, ",", "")
				}
			} else {
				s = strings.ReplaceAll(s, ",", "")
			}
		}
	}
	return s
}

func preprocessQuery(query string) string {
	processed := strings.ReplaceAll(query, "%", "/100.0")
	processed = numberRegex.ReplaceAllStringFunc(processed, normalizeNumberString)
	return processed
}

func (m *CalculatorModule) ProcessQuery(ctx context.Context, query string, apiCache *currency.APICache) ([]commontypes.FlowResult, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil, nil
	}

	processed := preprocessQuery(trimmed)

	program, err := expr.Compile(processed, expr.Env(m.mathEnv))
	if err != nil {
		return nil, nil
	}

	output, err := expr.Run(program, m.mathEnv)
	if err != nil {
		return nil, nil
	}

	var resultStr string
	switch v := output.(type) {
	case float64:
		resultStr = strconv.FormatFloat(v, 'f', 8, 64)
		resultStr = strings.TrimRight(resultStr, "0")
		resultStr = strings.TrimRight(resultStr, ".")
	case int:
		resultStr = strconv.Itoa(v)
	case int64:
		resultStr = strconv.FormatInt(v, 10)
	case bool:
		resultStr = strconv.FormatBool(v)
	default:
		return nil, nil
	}

	flowResult := commontypes.FlowResult{
		Title:    resultStr,
		SubTitle: fmt.Sprintf("Result for: %s", trimmed),
		IcoPath:  m.DefaultIconPath(),
		Score:    calculatorScore,
		JsonRPCAction: commontypes.JsonRPCAction{
			Method:     "copy_to_clipboard",
			Parameters: []interface{}{resultStr},
		},
	}

	return []commontypes.FlowResult{flowResult}, nil
}
