package currency // UPDATED package declaration

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	// No longer need to import "data" if CurrencyData is in the same package
	// "flow-http-receiver/modules/currency/data"
)

// ConversionRequest holds the parsed details from a user's query.
type ConversionRequest struct {
	Amount       float64
	FromCurrency string
	ToCurrency   string
}

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
			// This is common in many European locales.
			// Example: "1,23" -> 1.23; "123,456" -> 123.456 (if single comma); "1,234,567" -> 1234567
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
		// If a single comma remains and structure is like "X,Y" where Y isn't 1-3 digits, it's treated as X Y (no decimal)
		// e.g. "1,2345" -> "12345"
	}
	return s
}

func normalizeAndParseNumber(amountStr string) (float64, error) {
	normalizedStr := normalizeNumberString(amountStr)
	val, err := strconv.ParseFloat(normalizedStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number format '%s' (normalized to '%s')", amountStr, normalizedStr)
	}
	return val, nil
}

var (
	// Regex for amount part: \d[\d\s ,\.]*(?:[km]\b)?
	// \d[\d\s ,\.]*      -> matches the number, possibly with spaces (standard and non-breaking), dots, commas
	// (?:[km]\b)?        -> optionally matches 'k' or 'm' (kilo/million)
	//                      IF it's followed by a word boundary (e.g., space, end of string, punctuation).
	//                      This prevents 'k' in 'kzt' from being consumed as 'kilo'.
	amountRegexPart             = `\d[\d\s ,\.]*(?:[km]\b)?`
	symbolAndAmountRegexPart    = `(?:[$€₽¥£]|US\$|A\$|C\$|NZ\$|HK\$|S\$|CN¥|TL|zł|zl|kr|NOK|DKK|฿|R|₫|₩)?\s*` + amountRegexPart
	currencyTokenRegexPart      = `(?:[a-zA-Z]{1,10}|[$€₽¥£]|US\$|A\$|C\$|NZ\$|HK\$|S\$|CN¥|TL|zł|zl|kr|NOK|DKK|฿|R|₫|₩)` // Generic currency token
	currencyCodeStrictRegexPart = `[a-zA-Z]{3,10}`                                                                        // Stricter for currency codes

	// Amount (opt symbol, num with k/m) (opt op num with k/m) FromCurr (sep) ToCurr
	// MODIFIED: Added \b to "to" and "in" to ensure they are whole words and don't match prefixes.
	regexAmountCurrencyToCurrency = regexp.MustCompile(
		`(?i)^\s*(` + symbolAndAmountRegexPart + `)([\*\/]\s*` + amountRegexPart + `)?\s*(` + currencyTokenRegexPart + `)\s*(?:to\b|in\b|=|-?>|→|2)\s*(` + currencyTokenRegexPart + `)\s*$`)

	// NEW: Amount (opt symbol, num with k/m) (opt op num with k/m) SPACE FromToken SPACE ToToken
	// Example: "1 USD TON", "100 dollars euros"
	regexAmountSpacedTokens = regexp.MustCompile(
		`(?i)^\s*(` + symbolAndAmountRegexPart + `)([\*\/]\s*` + amountRegexPart + `)?\s+(` + currencyTokenRegexPart + `)\s+(` + currencyTokenRegexPart + `)\s*$`)

	// Amount (opt symbol, num with k/m) (opt op num with k/m) FromCurr (opt space) ToCurr (for 3+ char codes)
	// This uses currencyCodeStrictRegexPart for currency parts.
	regexAmountCurrencyCurrency = regexp.MustCompile(
		`(?i)^\s*(` + symbolAndAmountRegexPart + `)([\*\/]\s*` + amountRegexPart + `)?\s*(` + currencyCodeStrictRegexPart + `)\s*(` + currencyCodeStrictRegexPart + `)\s*$`)

	// Amount (opt symbol, num with k/m) (opt op num with k/m) FromCurr (for single currency specified)
	regexAmountCurrency = regexp.MustCompile(
		`(?i)^\s*(` + symbolAndAmountRegexPart + `)([\*\/]\s*` + amountRegexPart + `)?\s*(` + currencyTokenRegexPart + `)\s*$`)

	// Question format: "how much is AMOUNT CURR in CURR?" or "what is AMOUNT CURR?"
	regexQuestion = regexp.MustCompile(
		`(?i)^\s*(?:how\s+much\s+is|what\s*'?s|what\s+is)\s+(` + symbolAndAmountRegexPart + `)\s*(` + currencyTokenRegexPart + `)(?:\s+(?:in\b|to\b)\s+(` + currencyTokenRegexPart + `))?\??\s*$`)

	// "from/in AMOUNT CURR" or "from/in CURR AMOUNT"
	regexFromIn = regexp.MustCompile(
		`(?i)^\s*(?:from|in)\s+(?:(` + symbolAndAmountRegexPart + `)\s*(` + currencyTokenRegexPart + `)|(` + currencyTokenRegexPart + `)\s*(` + symbolAndAmountRegexPart + `))\s*$`)
)

func evaluateAmountExpression(amountPart1Str, opAmountPart2Str string) (float64, error) {
	evalStr := func(s string) (float64, error) {
		s = strings.ToLower(strings.TrimSpace(s))
		multiplier := 1.0
		// Suffixes 'k' or 'm' are handled here. Regex with `\b` should ensure `s` correctly includes them.
		if strings.HasSuffix(s, "k") {
			multiplier = 1000
			s = strings.TrimSuffix(s, "k")
		} else if strings.HasSuffix(s, "m") {
			multiplier = 1000000
			s = strings.TrimSuffix(s, "m")
		}
		// Remove common currency symbols if they are still attached to the number part for evalStr.
		// This is a safeguard; ideally, regex groups and ExtractSymbol separate these.
		// Order longer symbols first if they are prefixes of shorter ones (e.g., US$ before $)
		knownSymbols := []string{"us$", "a$", "c$", "nz$", "hk$", "s$", "cn¥", "tl", "zł", "zl", "nok", "dkk", "$", "€", "₽", "¥", "£", "kr", "฿", "r", "₫", "₩"}
		for _, sym := range knownSymbols {
			if strings.HasPrefix(s, strings.ToLower(sym)) { // Compare with lowercase symbol
				s = strings.TrimPrefix(s, strings.ToLower(sym))
				break // Remove only one symbol prefix
			}
		}
		s = strings.TrimSpace(s) // Trim space that might appear after symbol removal

		num, err := normalizeAndParseNumber(s)
		if err != nil {
			return 0, err
		}
		return num * multiplier, nil
	}

	val1, err := evalStr(amountPart1Str)
	if err != nil {
		return 0, fmt.Errorf("parsing amount part 1 '%s': %w", amountPart1Str, err)
	}

	if opAmountPart2Str == "" {
		return val1, nil
	}

	opAmountPart2Str = strings.TrimSpace(opAmountPart2Str)
	if len(opAmountPart2Str) < 2 { // Should have at least operator and one digit
		return 0, fmt.Errorf("invalid operation string '%s'", opAmountPart2Str)
	}
	op := opAmountPart2Str[0]
	val2Str := opAmountPart2Str[1:]

	val2, err := evalStr(val2Str)
	if err != nil {
		return 0, fmt.Errorf("parsing amount part 2 '%s': %w", val2Str, err)
	}

	switch op {
	case '*':
		return val1 * val2, nil
	case '/':
		if val2 == 0 {
			return 0, fmt.Errorf("division by zero")
		}
		return val1 / val2, nil
	default:
		return 0, fmt.Errorf("unsupported operator '%c'", op)
	}
}

// ParseQuery now takes *CurrencyData which is defined in data.go (same package)
func ParseQuery(query string, currencyData *CurrencyData) (*ConversionRequest, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is empty")
	}

	var matches []string
	var req ConversionRequest

	// Try regexAmountCurrencyToCurrency: "20 usd to rub", "€30 = ?$", "10rub→usd", "20usd2rub"
	matches = regexAmountCurrencyToCurrency.FindStringSubmatch(query)
	if len(matches) == 5 { // full match + 4 groups
		fromAmountStr := strings.TrimSpace(matches[1])
		opAmountStr := strings.TrimSpace(matches[2])
		fromCurrStr := strings.TrimSpace(matches[3])
		toCurrStr := strings.TrimSpace(matches[4])

		fromCurrStr, fromAmountStr = currencyData.ExtractSymbol(fromCurrStr, fromAmountStr)
		toCurrStr, _ = currencyData.ExtractSymbol(toCurrStr, "") // Resolve if 'toCurrStr' is a symbol like '₽' or '$'

		var err error
		req.Amount, err = evaluateAmountExpression(fromAmountStr, opAmountStr)
		if err != nil {
			return nil, fmt.Errorf("evaluating amount for AmountCurrencyToCurrency ('%s', op '%s'): %w", fromAmountStr, opAmountStr, err)
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

	// Try regexAmountSpacedTokens: "1 USD TON", "100 dollars euros"
	// This uses currencyTokenRegexPart for currency parts and requires spaces.
	matches = regexAmountSpacedTokens.FindStringSubmatch(query)
	if len(matches) == 5 { // full match + 4 groups (amount_op_group, amount_str_in_group1, from_curr_str, to_curr_str)
		fromAmountStr := strings.TrimSpace(matches[1]) // Amount part, potentially with symbol
		opAmountStr := strings.TrimSpace(matches[2])   // Operator part
		fromCurrStr := strings.TrimSpace(matches[3])   // From currency token
		toCurrStr := strings.TrimSpace(matches[4])     // To currency token

		fromCurrStr, fromAmountStr = currencyData.ExtractSymbol(fromCurrStr, fromAmountStr)
		toCurrStr, _ = currencyData.ExtractSymbol(toCurrStr, "") // Resolve if 'toCurrStr' is a symbol

		var err error
		req.Amount, err = evaluateAmountExpression(fromAmountStr, opAmountStr)
		if err != nil {
			return nil, fmt.Errorf("evaluating amount for AmountSpacedTokens ('%s', op '%s'): %w", fromAmountStr, opAmountStr, err)
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

	// Try regexAmountCurrencyCurrency: "50usdeur", "100 RUB JPY"
	// This uses currencyCodeStrictRegexPart for currency parts and optional spaces.
	matches = regexAmountCurrencyCurrency.FindStringSubmatch(query)
	if len(matches) == 5 { // full match + 4 groups
		fromAmountStr := strings.TrimSpace(matches[1])
		opAmountStr := strings.TrimSpace(matches[2])
		fromCurrStr := strings.TrimSpace(matches[3])
		toCurrStr := strings.TrimSpace(matches[4])

		fromCurrStr, fromAmountStr = currencyData.ExtractSymbol(fromCurrStr, fromAmountStr)
		// No ExtractSymbol for toCurrStr here, as it's expected to be a strict code. ResolveCurrency handles it.

		var err error
		req.Amount, err = evaluateAmountExpression(fromAmountStr, opAmountStr)
		if err != nil {
			return nil, fmt.Errorf("evaluating amount for AmountCurrencyCurrency ('%s', op '%s'): %w", fromAmountStr, opAmountStr, err)
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

	// Try regexQuestion: "how much is 100 dollars in rubles?", "what is 30 yuan?"
	// MODIFIED: Added \b to "in" and "to" in the optional "in/to TO_CURRENCY" part
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
		req.Amount, err = evaluateAmountExpression(amountStr, "")
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

	// Try regexFromIn: "from 50 rub", "in euros 75"
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
		req.Amount, err = evaluateAmountExpression(amountStr, "")
		if err != nil {
			return nil, fmt.Errorf("evaluating amount for FromIn ('%s'): %w", amountStr, err)
		}
		req.FromCurrency, err = currencyData.ResolveCurrency(currStr)
		if err != nil {
			return nil, fmt.Errorf("resolving from_currency '%s' (FromIn): %w", currStr, err)
		}
		return &req, nil
	}

	// Try regexAmountCurrency: "20 usd", "$50" (implies quick/base conversion)
	matches = regexAmountCurrency.FindStringSubmatch(query)
	if len(matches) == 4 { // full match + 3 groups (amount_op_group, amount, from_currency)
		fromAmountStrRaw := strings.TrimSpace(matches[1])
		opAmountStr := strings.TrimSpace(matches[2])
		fromCurrStrCandidate := strings.TrimSpace(matches[3])

		resolvedCurrStr, finalAmountStr := currencyData.ExtractSymbol(fromCurrStrCandidate, fromAmountStrRaw)

		var err error
		req.Amount, err = evaluateAmountExpression(finalAmountStr, opAmountStr)
		if err != nil {
			return nil, fmt.Errorf("evaluating amount for AmountCurrency ('%s', op '%s'): %w", finalAmountStr, opAmountStr, err)
		}

		req.FromCurrency, err = currencyData.ResolveCurrency(resolvedCurrStr)
		if err != nil {
			return nil, fmt.Errorf("resolving from_currency '%s' (AmountCurrency): %w", resolvedCurrStr, err)
		}
		return &req, nil
	}

	return nil, fmt.Errorf("query '%s' did not match any currency conversion pattern", query)
}
