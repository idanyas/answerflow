package currency

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/expr-lang/expr"
)

// ConversionRequest holds the parsed details from a user's query.
type ConversionRequest struct {
	Amount       float64
	FromCurrency string
	ToCurrency   string
}

// Regex to find numbers that may have separators and k/m suffixes.
var numberWithSuffixRegex = regexp.MustCompile(`[0-9]+(?:[0-9\s ,.]*[0-9])?(?:[km]\b)?`)

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

// preprocessAmountExpression normalizes number formats and suffixes (k/m) within an expression string,
// preparing it for evaluation by the 'expr' library.
func preprocessAmountExpression(exprStr string) string {
	return numberWithSuffixRegex.ReplaceAllStringFunc(exprStr, func(match string) string {
		numPart := match
		multiplierSuffix := ""
		if strings.HasSuffix(numPart, "k") {
			multiplierSuffix = "*1000"
			numPart = strings.TrimSuffix(numPart, "k")
		} else if strings.HasSuffix(numPart, "m") {
			multiplierSuffix = "*1000000"
			numPart = strings.TrimSuffix(numPart, "m")
		}

		normalizedNum := normalizeNumberString(numPart)

		return normalizedNum + multiplierSuffix
	})
}

// evaluateAmountExpression takes a mathematical expression string, processes it,
// and evaluates it to a float64 result.
func evaluateAmountExpression(expressionStr string) (float64, error) {
	// Cleanup: remove known currency symbols from the expression string.
	// This is a safeguard, assuming ExtractSymbol has done its main job.
	cleanExpr := strings.ToLower(strings.TrimSpace(expressionStr))
	knownSymbols := []string{"us$", "a$", "c$", "nz$", "hk$", "s$", "cn¥", "tl", "zł", "zl", "nok", "dkk", "$", "€", "₽", "¥", "£", "kr", "฿", "r", "₫", "₩"}
	for _, sym := range knownSymbols {
		cleanExpr = strings.ReplaceAll(cleanExpr, strings.ToLower(sym), "")
	}
	cleanExpr = strings.TrimSpace(cleanExpr)
	if cleanExpr == "" {
		return 0, fmt.Errorf("amount expression is empty after cleaning symbols")
	}

	processedExpr := preprocessAmountExpression(cleanExpr)

	// Compile and run the expression. No special env needed for basic arithmetic.
	program, err := expr.Compile(processedExpr, expr.Env(nil))
	if err != nil {
		return 0, fmt.Errorf("compiling expression '%s' (processed to '%s'): %w", expressionStr, processedExpr, err)
	}

	output, err := expr.Run(program, nil)
	if err != nil {
		return 0, fmt.Errorf("running expression '%s' (processed to '%s'): %w", expressionStr, processedExpr, err)
	}

	// Convert result to float64
	switch v := output.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	default:
		return 0, fmt.Errorf("expression result is not a number, but %T", v)
	}
}

var (
	// Regex for a single number, possibly with separators and a k/m suffix.
	amountRegexPart = `[0-9]+(?:[0-9\s ,.]*[0-9])?(?:[km]\b)?`
	// Regex for a mathematical expression containing numbers and operators (*, /).
	amountExpressionPart = amountRegexPart + `(?:\s*[*\/]\s*` + amountRegexPart + `)*`
	// Optional currency symbol prefix.
	symbolPrefixPart = `(?:[$€₽¥£]|US\$|A\$|C\$|NZ\$|HK\$|S\$|CN¥|TL|zł|zl|kr|NOK|DKK|฿|R|₫|₩)?`
	// The complete amount expression part, including an optional symbol prefix.
	fullAmountExpressionPart = symbolPrefixPart + `\s*` + amountExpressionPart

	currencyTokenRegexPart      = `(?:[a-zA-Z]{1,10}|[$€₽¥£]|US\$|A\$|C\$|NZ\$|HK\$|S\$|CN¥|TL|zł|zl|kr|NOK|DKK|฿|R|₫|₩)` // Generic currency token
	currencyCodeStrictRegexPart = `[a-zA-Z]{3,10}`

	// Regexes updated to use `fullAmountExpressionPart` to capture the entire mathematical expression.

	// AmountExpr FromCurr (sep) ToCurr
	regexAmountCurrencyToCurrency = regexp.MustCompile(
		`(?i)^\s*(` + fullAmountExpressionPart + `)\s*(` + currencyTokenRegexPart + `)\s*(?:to\b|in\b|=|-?>|→|2)\s*(` + currencyTokenRegexPart + `)\s*$`)

	// AmountExpr SPACE FromToken SPACE ToToken
	regexAmountSpacedTokens = regexp.MustCompile(
		`(?i)^\s*(` + fullAmountExpressionPart + `)\s+(` + currencyTokenRegexPart + `)\s+(` + currencyTokenRegexPart + `)\s*$`)

	// AmountExpr FromCurr (opt space) ToCurr (for 3+ char codes)
	regexAmountCurrencyCurrency = regexp.MustCompile(
		`(?i)^\s*(` + fullAmountExpressionPart + `)\s*(` + currencyCodeStrictRegexPart + `)\s*(` + currencyCodeStrictRegexPart + `)\s*$`)

	// AmountExpr FromCurr
	regexAmountCurrency = regexp.MustCompile(
		`(?i)^\s*(` + fullAmountExpressionPart + `)\s*(` + currencyTokenRegexPart + `)\s*$`)

	// "how much is AmountExpr CURR in CURR?"
	regexQuestion = regexp.MustCompile(
		`(?i)^\s*(?:how\s+much\s+is|what\s*'?s|what\s+is)\s+(` + fullAmountExpressionPart + `)\s*(` + currencyTokenRegexPart + `)(?:\s+(?:in\b|to\b)\s+(` + currencyTokenRegexPart + `))?\??\s*$`)

	// "from/in AmountExpr CURR" or "from/in CURR AmountExpr"
	regexFromIn = regexp.MustCompile(
		`(?i)^\s*(?:from|in)\s+(?:(` + fullAmountExpressionPart + `)\s*(` + currencyTokenRegexPart + `)|(` + currencyTokenRegexPart + `)\s*(` + fullAmountExpressionPart + `))\s*$`)
)

// ParseQuery now takes *CurrencyData which is defined in data.go (same package)
func ParseQuery(query string, currencyData *CurrencyData) (*ConversionRequest, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is empty")
	}

	var matches []string
	var req ConversionRequest

	// Try regexAmountCurrencyToCurrency: "1000*0.9/97 usd to rub", "€30 = ?$", "10rub→usd"
	matches = regexAmountCurrencyToCurrency.FindStringSubmatch(query)
	if len(matches) == 4 { // full match + 3 groups: amount_expr, from_curr, to_curr
		amountExprStr := strings.TrimSpace(matches[1])
		fromCurrStr := strings.TrimSpace(matches[2])
		toCurrStr := strings.TrimSpace(matches[3])

		fromCurrStr, amountExprStr = currencyData.ExtractSymbol(fromCurrStr, amountExprStr)
		toCurrStr, _ = currencyData.ExtractSymbol(toCurrStr, "") // Resolve if 'toCurrStr' is a symbol like '₽' or '$'

		var err error
		req.Amount, err = evaluateAmountExpression(amountExprStr)
		if err != nil {
			return nil, fmt.Errorf("evaluating amount for AmountCurrencyToCurrency ('%s'): %w", amountExprStr, err)
		}

		req.FromCurrency, err = currencyData.ResolveCurrency(fromCurrStr)
		if err != nil {
			return nil, fmt.Errorf("resolving from_currency '%s' (AmountCurrencyToCurrency): %w", fromCurrStr, err)
		}
		req.ToCurrency, err = currencyData.ResolveCurrency(toCurrStr)
		if err != nil {
			return nil, fmt.Errorf("resolving to_currency '%s' (AmountCurrencyToCurrency): %w", toCurrStr, err)
		}
		return &req, nil
	}

	// Try regexAmountSpacedTokens: "10*10 USD TON", "100 dollars euros"
	matches = regexAmountSpacedTokens.FindStringSubmatch(query)
	if len(matches) == 4 { // full match + 3 groups: amount_expr, from_curr, to_curr
		amountExprStr := strings.TrimSpace(matches[1])
		fromCurrStr := strings.TrimSpace(matches[2])
		toCurrStr := strings.TrimSpace(matches[3])

		fromCurrStr, amountExprStr = currencyData.ExtractSymbol(fromCurrStr, amountExprStr)
		toCurrStr, _ = currencyData.ExtractSymbol(toCurrStr, "") // Resolve if 'toCurrStr' is a symbol

		var err error
		req.Amount, err = evaluateAmountExpression(amountExprStr)
		if err != nil {
			return nil, fmt.Errorf("evaluating amount for AmountSpacedTokens ('%s'): %w", amountExprStr, err)
		}

		req.FromCurrency, err = currencyData.ResolveCurrency(fromCurrStr)
		if err != nil {
			return nil, fmt.Errorf("resolving from_currency '%s' (AmountSpacedTokens): %w", fromCurrStr, err)
		}
		req.ToCurrency, err = currencyData.ResolveCurrency(toCurrStr)
		if err != nil {
			return nil, fmt.Errorf("resolving to_currency '%s' (AmountSpacedTokens): %w", toCurrStr, err)
		}
		return &req, nil
	}

	// Try regexAmountCurrencyCurrency: "50*2usdeur", "100 RUB JPY"
	matches = regexAmountCurrencyCurrency.FindStringSubmatch(query)
	if len(matches) == 4 { // full match + 3 groups: amount_expr, from_curr, to_curr
		amountExprStr := strings.TrimSpace(matches[1])
		fromCurrStr := strings.TrimSpace(matches[2])
		toCurrStr := strings.TrimSpace(matches[3])

		fromCurrStr, amountExprStr = currencyData.ExtractSymbol(fromCurrStr, amountExprStr)
		// No ExtractSymbol for toCurrStr here, as it's expected to be a strict code. ResolveCurrency handles it.

		var err error
		req.Amount, err = evaluateAmountExpression(amountExprStr)
		if err != nil {
			return nil, fmt.Errorf("evaluating amount for AmountCurrencyCurrency ('%s'): %w", amountExprStr, err)
		}
		req.FromCurrency, err = currencyData.ResolveCurrency(fromCurrStr)
		if err != nil {
			return nil, fmt.Errorf("resolving from_currency '%s' (AmountCurrencyCurrency): %w", fromCurrStr, err)
		}
		req.ToCurrency, err = currencyData.ResolveCurrency(toCurrStr)
		if err != nil {
			return nil, fmt.Errorf("resolving to_currency '%s' (AmountCurrencyCurrency): %w", toCurrStr, err)
		}
		return &req, nil
	}

	// Try regexQuestion: "how much is 100*5 dollars in rubles?", "what is 30 yuan?"
	matches = regexQuestion.FindStringSubmatch(query)
	if len(matches) > 0 { // matches can be 3 or 4 depending on whether ToCurrency is present
		amountStr := strings.TrimSpace(matches[1])
		fromCurrStr := strings.TrimSpace(matches[2])
		toCurrStr := ""
		if len(matches) == 4 && matches[3] != "" { // Group 3 is optional (ToCurrency)
			toCurrStr = strings.TrimSpace(matches[3])
		}

		fromCurrStr, amountStr = currencyData.ExtractSymbol(fromCurrStr, amountStr)
		if toCurrStr != "" {
			toCurrStr, _ = currencyData.ExtractSymbol(toCurrStr, "")
		}

		var err error
		req.Amount, err = evaluateAmountExpression(amountStr)
		if err != nil {
			return nil, fmt.Errorf("evaluating amount for Question ('%s'): %w", amountStr, err)
		}
		req.FromCurrency, err = currencyData.ResolveCurrency(fromCurrStr)
		if err != nil {
			return nil, fmt.Errorf("resolving from_currency '%s' (Question): %w", fromCurrStr, err)
		}
		if toCurrStr != "" {
			req.ToCurrency, err = currencyData.ResolveCurrency(toCurrStr)
			if err != nil {
				return nil, fmt.Errorf("resolving to_currency '%s' (Question): %w", toCurrStr, err)
			}
		}
		return &req, nil
	}

	// Try regexFromIn: "from 50*2 rub", "in euros 75"
	matches = regexFromIn.FindStringSubmatch(query)
	if len(matches) > 0 { // matches will have 5 elements: full, (amt, curr) OR (curr, amt)
		var amountStr, currStr string
		if matches[1] != "" && matches[2] != "" { // (amount) (currency)
			amountStr = strings.TrimSpace(matches[1])
			currStr = strings.TrimSpace(matches[2])
		} else if matches[3] != "" && matches[4] != "" { // (currency) (amount)
			currStr = strings.TrimSpace(matches[3])
			amountStr = strings.TrimSpace(matches[4])
		} else {
			return nil, fmt.Errorf("malformed 'from/in' query structure")
		}

		currStr, amountStr = currencyData.ExtractSymbol(currStr, amountStr)

		var err error
		req.Amount, err = evaluateAmountExpression(amountStr)
		if err != nil {
			return nil, fmt.Errorf("evaluating amount for FromIn ('%s'): %w", amountStr, err)
		}
		req.FromCurrency, err = currencyData.ResolveCurrency(currStr)
		if err != nil {
			return nil, fmt.Errorf("resolving from_currency '%s' (FromIn): %w", currStr, err)
		}
		return &req, nil
	}

	// Try regexAmountCurrency: "1000*0.925/97.09 usd", "$50" (implies quick/base conversion)
	matches = regexAmountCurrency.FindStringSubmatch(query)
	if len(matches) == 3 { // full match + 2 groups: amount_expr, from_currency
		amountExprStr := strings.TrimSpace(matches[1])
		fromCurrStrCandidate := strings.TrimSpace(matches[2])

		resolvedCurrStr, finalAmountStr := currencyData.ExtractSymbol(fromCurrStrCandidate, amountExprStr)

		var err error
		req.Amount, err = evaluateAmountExpression(finalAmountStr)
		if err != nil {
			return nil, fmt.Errorf("evaluating amount for AmountCurrency ('%s'): %w", finalAmountStr, err)
		}

		req.FromCurrency, err = currencyData.ResolveCurrency(resolvedCurrStr)
		if err != nil {
			return nil, fmt.Errorf("resolving from_currency '%s' (AmountCurrency): %w", resolvedCurrStr, err)
		}
		return &req, nil
	}

	return nil, fmt.Errorf("query '%s' did not match any currency conversion pattern", query)
}
