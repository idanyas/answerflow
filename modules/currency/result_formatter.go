// modules/currency/result_formatter.go
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
	// For inverse, we calculated sourceAmount to get targetAmount. The rate is how much source is needed for 1 unit of target.
	marketRate := sourceAmount / targetAmount

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
			// source is RUB, target is USD. marketRate is RUB/USD. Correct for display.
			rateStr = fmt.Sprintf("1 USD = %s RUB", formatRate(marketRate))
		} else if hasUsdSource && hasRubTarget {
			// source is USD, target is RUB. marketRate is USD/RUB. Need to invert for display.
			if marketRate > 0 {
				rateStr = fmt.Sprintf("1 USD = %s RUB", formatRate(1.0/marketRate))
			}
		}
	} else if marketRate > 0 && !math.IsNaN(marketRate) && !math.IsInf(marketRate, 0) {
		// Rate should be "1 TARGET = X SOURCE"
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
