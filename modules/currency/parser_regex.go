package currency

import "regexp"

var (
	amountRegexPart          = `[0-9]+(?:[0-9\s ,.]*[0-9])?(?:[km]\b)?`
	amountExpressionPart     = amountRegexPart + `(?:\s*[*\/]\s*` + amountRegexPart + `)*`
	symbolPrefixPart         = `(?:[$€₽¥£]|US\$|A\$|C\$|NZ\$|HK\$|S\$|CN¥|TL|zł|zl|kr|NOK|DKK|฿|R|₫|₩)?`
	fullAmountExpressionPart = symbolPrefixPart + `\s*` + amountExpressionPart
	currencyTokenRegexPart   = `(?:[a-zA-Z]{1,10}|[$€₽¥£]|US\$|A\$|C\$|NZ\$|HK\$|S\$|CN¥|TL|zł|zl|kr|NOK|DKK|฿|R|₫|₩)`
	currencyCodeStrictPart   = `[a-zA-Z]{3,10}`
)

var (
	regexAmountCurrencyToCurrency = regexp.MustCompile(
		`(?i)^\s*(` + fullAmountExpressionPart + `)\s*(` + currencyTokenRegexPart + `)\s*(?:to\b|in\b|=|-?>|→|2)\s*(` + currencyTokenRegexPart + `)\s*$`)

	regexAmountSpacedTokens = regexp.MustCompile(
		`(?i)^\s*(` + fullAmountExpressionPart + `)\s+(` + currencyTokenRegexPart + `)\s+(` + currencyTokenRegexPart + `)\s*$`)

	regexAmountCurrencyCurrency = regexp.MustCompile(
		`(?i)^\s*(` + fullAmountExpressionPart + `)\s*(` + currencyCodeStrictPart + `)\s*(` + currencyCodeStrictPart + `)\s*$`)

	regexAmountCurrency = regexp.MustCompile(
		`(?i)^\s*(` + fullAmountExpressionPart + `)\s*(` + currencyTokenRegexPart + `)\s*$`)

	regexQuestion = regexp.MustCompile(
		`(?i)^\s*(?:how\s+much\s+is|what\s*'?s|what\s+is)\s+(` + fullAmountExpressionPart + `)\s*(` + currencyTokenRegexPart + `)(?:\s+(?:in\b|to\b)\s+(` + currencyTokenRegexPart + `))?\??\s*$`)

	regexFromIn = regexp.MustCompile(
		`(?i)^\s*(?:from|in)\s+(?:(` + fullAmountExpressionPart + `)\s*(` + currencyTokenRegexPart + `)|(` + currencyTokenRegexPart + `)\s*(` + fullAmountExpressionPart + `))\s*$`)

	numberWithSuffixRegex = regexp.MustCompile(`[0-9]+(?:[0-9\s ,.]*[0-9])?(?:[km]\b)?`)
)
