package currency

import (
	"fmt"
	"math"

	"answerflow/commontypes"
)

func (m *CurrencyConverterModule) formatResult(req *ConversionRequest, targetCurrency string, finalAmount, displayRate float64, score int, slippageInfo string) *commontypes.FlowResult {
	var title, subTitle string

	hasRubFrom := req.FromCurrency == "RUB"
	hasRubTo := targetCurrency == "RUB"
	hasUsdFrom := req.FromCurrency == "USD" || req.FromCurrency == "USDT"
	hasUsdTo := targetCurrency == "USD" || targetCurrency == "USDT"

	var tag string
	if (hasRubFrom && hasUsdTo) || (hasUsdFrom && hasRubTo) {
		if hasRubFrom && hasUsdTo {
			tag = " 🏷️ продать"
		} else if hasUsdFrom && hasRubTo {
			tag = " 🛍️ купить"
		}
	}

	clipboardText := formatAmountForClipboard(finalAmount, targetCurrency)
	formattedAmount := formatAmount(finalAmount, targetCurrency)

	if m.ShortDisplayFormat {
		title = fmt.Sprintf("%s %s", formattedAmount, targetCurrency)
	} else {
		title = fmt.Sprintf("%s %s = %s %s",
			formatAmount(req.Amount, req.FromCurrency), req.FromCurrency,
			formattedAmount, targetCurrency)
	}

	var rateStr string
	if (hasRubFrom || hasRubTo) && (hasUsdFrom || hasUsdTo) {
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

	subTitle = rateStr + tag + slippageInfo

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
	marketRate := sourceAmount / targetAmount

	hasRub := sourceCurrency == "RUB" || targetCurrency == "RUB"
	hasUsd := sourceCurrency == "USD" || sourceCurrency == "USDT" || targetCurrency == "USD" || targetCurrency == "USDT"

	var tag, rateStr string
	if hasRub && hasUsd {
		if sourceCurrency == "USD" && targetCurrency == "RUB" {
			tag = " 🛍️ купить"
			rateStr = fmt.Sprintf("1 RUB = %s USD", formatRate(1.0/marketRate))
		} else if sourceCurrency == "RUB" && targetCurrency == "USD" {
			tag = " 🏷️ продать"
			rateStr = fmt.Sprintf("1 USD = %s RUB", formatRate(marketRate))
		}
	} else if marketRate > 0 && !math.IsNaN(marketRate) && !math.IsInf(marketRate, 0) {
		rateStr = fmt.Sprintf("1 %s = %s %s", targetCurrency, formatRate(marketRate), sourceCurrency)
	}

	clipboardText := formatAmountForClipboard(sourceAmount, sourceCurrency)
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
