package currency

import (
	"regexp"
	"strings"
)

func NormalizeNumberString(s string) string {
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

func TranslateError(err error) string {
	if err == nil {
		return ""
	}

	errMsg := err.Error()

	translations := map[string]string{
		"circuit breaker":                             "service temporarily unavailable, please try again in a few minutes",
		"rate: Wait":                                  "service temporarily busy, please try again",
		"context deadline exceeded":                   "request timed out, please try again",
		"whitebird service temporarily unavailable":   "RUB exchange temporarily unavailable, please try again later",
		"bybit service unavailable":                   "cryptocurrency exchange temporarily unavailable, please try again",
		"fiat exchange rates temporarily unavailable": "fiat currency rates temporarily unavailable, please try again later",
		"exchange rate not available":                 "exchange rate information is updating, please try again",
		"insufficient liquidity":                      "this amount is too large for current market conditions",
		"amount too small after":                      "amount too small - fees would consume all value",
		"no match":                                    "could not parse currency query",
		"unknown currency":                            "currency not recognized",
	}

	for pattern, friendly := range translations {
		if strings.Contains(errMsg, pattern) {
			return friendly
		}
	}

	return errMsg
}
