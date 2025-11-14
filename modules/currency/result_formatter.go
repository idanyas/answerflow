package currency

import (
	"fmt"
	"math"

	"answerflow/commontypes"
)

func (m *CurrencyConverterModule) formatResult(req *ConversionRequest, targetCurrency string, finalAmount, displayRate float64, score int, slippageInfo string, feesInfo string) *commontypes.FlowResult {
	var title, subTitle string

	hasRubFrom := req.FromCurrency == "RUB"
	hasRubTo := targetCurrency == "RUB"
	hasUsdFrom := req.FromCurrency == "USD" || req.FromCurrency == "USDT"
	hasUsdTo := targetCurrency == "USD" || targetCurrency == "USDT"

	// ALWAYS determine buy/sell tag based on RUB relationship
	var tag string
	if hasRubFrom {
		// FROM RUB: buying foreign currency
		tag = " 🛍️ купить"
	} else if hasRubTo {
		// TO RUB: selling foreign currency for RUB
		tag = " 🏷️ продать"
	} else {
		// Foreign to Foreign: selling foreign currency (could ultimately be sold to RUB)
		tag = " 🏷️ продать"
	}

	clipboardText := fmt.Sprintf("%s %s", formatAmountForClipboard(finalAmount, targetCurrency), targetCurrency)
	formattedAmount := formatAmount(finalAmount, targetCurrency)

	if m.ShortDisplayFormat {
		title = fmt.Sprintf("%s %s", formattedAmount, targetCurrency)
	} else {
		title = fmt.Sprintf("%s %s = %s %s",
			formatAmount(req.Amount, req.FromCurrency), req.FromCurrency,
			formattedAmount, targetCurrency)
	}

	// Rate display with special handling for RUB<->USD pairs
	var rateStr string
	if (hasRubFrom || hasRubTo) && (hasUsdFrom || hasUsdTo) {
		// Special display for RUB<->USD: always show "1 USD = X RUB"
		if hasRubFrom && hasUsdTo {
			if displayRate > 0 {
				rateStr = fmt.Sprintf("1 USD = %s RUB", formatRate(1.0/displayRate))
			}
		} else if hasUsdFrom && hasRubTo {
			rateStr = fmt.Sprintf("1 USD = %s RUB", formatRate(displayRate))
		}
	} else {
		rateStr = fmt.Sprintf("1 %s = %s %s", req.FromCurrency, formatRate(displayRate), targetCurrency)
	}

	subTitle = rateStr + tag + slippageInfo + feesInfo

	return &commontypes.FlowResult{
		Title:    title,
		SubTitle: subTitle,
		Score:    score,
		JsonRPCAction: commontypes.JsonRPCAction{
			Method:     "copy_to_clipboard",
			Parameters: []interface{}{clipboardText},
		},
	}
}

func (m *CurrencyConverterModule) formatInverseResult(sourceAmount float64, sourceCurrency string, targetAmount float64, targetCurrency string, score int) *commontypes.FlowResult {
	// marketRate represents the exchange rate between currencies
	// For inverse: we calculated sourceAmount needed to get targetAmount
	// Example: 1.32 USD needed for 100 RUB means rate = 100/1.32 = 75.76 RUB per USD
	marketRate := targetAmount / sourceAmount

	hasRubSource := sourceCurrency == "RUB"
	hasRubTarget := targetCurrency == "RUB"
	hasUsdSource := sourceCurrency == "USD" || sourceCurrency == "USDT"
	hasUsdTarget := targetCurrency == "USD" || targetCurrency == "USDT"

	// ALWAYS determine buy/sell tag based on RUB relationship
	var tag string
	if hasRubSource {
		// Source is RUB: spending RUB to buy foreign currency
		tag = " 🛍️ купить"
	} else if hasRubTarget {
		// Target is RUB: getting RUB from foreign currency
		tag = " 🏷️ продать"
	} else {
		// Foreign to foreign inverse: buying foreign currency (would need RUB first)
		tag = " 🛍️ купить"
	}

	// Rate display with special handling for RUB<->USD pairs
	var rateStr string
	if (hasRubSource || hasRubTarget) && (hasUsdSource || hasUsdTarget) {
		// Special display for RUB<->USD: always show "1 USD = X RUB"
		if hasRubSource && hasUsdTarget {
			// RUB -> USD: marketRate = targetUSD / sourceRUB, so 1 USD = 1/marketRate RUB
			rateStr = fmt.Sprintf("1 USD = %s RUB", formatRate(1.0/marketRate))
		} else if hasUsdSource && hasRubTarget {
			// USD -> RUB: marketRate = targetRUB / sourceUSD, so 1 USD = marketRate RUB
			rateStr = fmt.Sprintf("1 USD = %s RUB", formatRate(marketRate))
		} else if hasRubTarget && hasUsdSource {
			rateStr = fmt.Sprintf("1 USD = %s RUB", formatRate(marketRate))
		} else if hasRubSource && hasUsdTarget {
			rateStr = fmt.Sprintf("1 USD = %s RUB", formatRate(1.0/marketRate))
		}
	} else if marketRate > 0 && !math.IsNaN(marketRate) && !math.IsInf(marketRate, 0) {
		rateStr = fmt.Sprintf("1 %s = %s %s", targetCurrency, formatRate(marketRate), sourceCurrency)
	}

	clipboardText := fmt.Sprintf("%s %s", formatAmountForClipboard(sourceAmount, sourceCurrency), sourceCurrency)
	formattedSource := formatAmount(sourceAmount, sourceCurrency)

	var title string
	if m.ShortDisplayFormat {
		title = fmt.Sprintf("%s %s", formattedSource, sourceCurrency)
	} else {
		title = fmt.Sprintf("%s %s = %s %s",
			formattedSource, sourceCurrency,
			formatAmount(targetAmount, targetCurrency), targetCurrency)
	}

	return &commontypes.FlowResult{
		Title:    title,
		SubTitle: rateStr + tag,
		Score:    score,
		JsonRPCAction: commontypes.JsonRPCAction{
			Method:     "copy_to_clipboard",
			Parameters: []interface{}{clipboardText},
		},
	}
}
