package currency

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/expr-lang/expr"
)

type ConversionRequest struct {
	Amount       float64
	FromCurrency string
	ToCurrency   string
}

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

func preprocessAmountExpression(exprStr string) string {
	return numberWithSuffixRegex.ReplaceAllStringFunc(exprStr, func(match string) string {
		numPart := match
		multiplier := ""
		if strings.HasSuffix(numPart, "k") {
			multiplier = "*1000"
			numPart = strings.TrimSuffix(numPart, "k")
		} else if strings.HasSuffix(numPart, "m") {
			multiplier = "*1000000"
			numPart = strings.TrimSuffix(numPart, "m")
		}
		return normalizeNumberString(numPart) + multiplier
	})
}

func evaluateAmountExpression(expressionStr string) (float64, error) {
	cleanExpr := strings.ToLower(strings.TrimSpace(expressionStr))
	knownSymbols := []string{"us$", "a$", "c$", "nz$", "hk$", "s$", "cn¥", "tl", "zł", "zl", "nok", "dkk", "$", "€", "₽", "¥", "£", "kr", "฿", "r", "₫", "₩"}
	for _, sym := range knownSymbols {
		cleanExpr = strings.ReplaceAll(cleanExpr, strings.ToLower(sym), "")
	}
	cleanExpr = strings.TrimSpace(cleanExpr)
	if cleanExpr == "" {
		return 0, fmt.Errorf("empty expression")
	}

	processedExpr := preprocessAmountExpression(cleanExpr)

	program, err := expr.Compile(processedExpr, expr.Env(nil))
	if err != nil {
		return 0, err
	}

	output, err := expr.Run(program, nil)
	if err != nil {
		return 0, err
	}

	switch v := output.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	default:
		return 0, fmt.Errorf("not a number")
	}
}

func ParseQuery(query string, currencyData *CurrencyData) (*ConversionRequest, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}

	var req ConversionRequest

	if matches := regexAmountCurrencyToCurrency.FindStringSubmatch(query); len(matches) == 4 {
		return parseMatch(matches, currencyData, &req, 3)
	}

	if matches := regexAmountSpacedTokens.FindStringSubmatch(query); len(matches) == 4 {
		return parseMatch(matches, currencyData, &req, 3)
	}

	if matches := regexAmountCurrencyCurrency.FindStringSubmatch(query); len(matches) == 4 {
		return parseMatch(matches, currencyData, &req, 3)
	}

	if matches := regexQuestion.FindStringSubmatch(query); len(matches) > 0 {
		amountStr := strings.TrimSpace(matches[1])
		fromCurrStr := strings.TrimSpace(matches[2])
		toCurrStr := ""
		if len(matches) == 4 && matches[3] != "" {
			toCurrStr = strings.TrimSpace(matches[3])
		}

		fromCurrStr, amountStr = currencyData.ExtractSymbol(fromCurrStr, amountStr)
		if toCurrStr != "" {
			toCurrStr, _ = currencyData.ExtractSymbol(toCurrStr, "")
		}

		var err error
		req.Amount, err = evaluateAmountExpression(amountStr)
		if err != nil {
			return nil, err
		}
		req.FromCurrency, err = currencyData.ResolveCurrency(fromCurrStr)
		if err != nil {
			return nil, err
		}
		if toCurrStr != "" {
			req.ToCurrency, err = currencyData.ResolveCurrency(toCurrStr)
			if err != nil {
				return nil, err
			}
		}
		return &req, nil
	}

	if matches := regexFromIn.FindStringSubmatch(query); len(matches) > 0 {
		var amountStr, currStr string
		if matches[1] != "" && matches[2] != "" {
			amountStr = strings.TrimSpace(matches[1])
			currStr = strings.TrimSpace(matches[2])
		} else if matches[3] != "" && matches[4] != "" {
			currStr = strings.TrimSpace(matches[3])
			amountStr = strings.TrimSpace(matches[4])
		} else {
			return nil, fmt.Errorf("malformed query")
		}

		currStr, amountStr = currencyData.ExtractSymbol(currStr, amountStr)

		var err error
		req.Amount, err = evaluateAmountExpression(amountStr)
		if err != nil {
			return nil, err
		}
		req.FromCurrency, err = currencyData.ResolveCurrency(currStr)
		if err != nil {
			return nil, err
		}
		return &req, nil
	}

	if matches := regexAmountCurrency.FindStringSubmatch(query); len(matches) == 3 {
		amountExprStr := strings.TrimSpace(matches[1])
		fromCurrStrCandidate := strings.TrimSpace(matches[2])

		resolvedCurrStr, finalAmountStr := currencyData.ExtractSymbol(fromCurrStrCandidate, amountExprStr)

		var err error
		req.Amount, err = evaluateAmountExpression(finalAmountStr)
		if err != nil {
			return nil, err
		}
		req.FromCurrency, err = currencyData.ResolveCurrency(resolvedCurrStr)
		if err != nil {
			return nil, err
		}
		return &req, nil
	}

	return nil, fmt.Errorf("no match")
}

func parseMatch(matches []string, currencyData *CurrencyData, req *ConversionRequest, groups int) (*ConversionRequest, error) {
	amountExprStr := strings.TrimSpace(matches[1])
	fromCurrStr := strings.TrimSpace(matches[2])
	toCurrStr := ""
	if groups == 3 {
		toCurrStr = strings.TrimSpace(matches[3])
	}

	fromCurrStr, amountExprStr = currencyData.ExtractSymbol(fromCurrStr, amountExprStr)
	if toCurrStr != "" {
		toCurrStr, _ = currencyData.ExtractSymbol(toCurrStr, "")
	}

	var err error
	req.Amount, err = evaluateAmountExpression(amountExprStr)
	if err != nil {
		return nil, err
	}
	req.FromCurrency, err = currencyData.ResolveCurrency(fromCurrStr)
	if err != nil {
		return nil, err
	}
	if toCurrStr != "" {
		req.ToCurrency, err = currencyData.ResolveCurrency(toCurrStr)
		if err != nil {
			return nil, err
		}
	}
	return req, nil
}
