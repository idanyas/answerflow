package currency

import (
	"fmt"
	"log"
	"math"

	"answerflow/commontypes"
)

func (m *CurrencyConverterModule) generateConversionResult(req *ConversionRequest, targetCurrency string, apiCache *APICache, baseScore int) (*commontypes.FlowResult, float64, error) {
	fromCurrency := req.FromCurrency
	if fromCurrency == targetCurrency {
		return nil, 0, nil
	}

	// Convert req.Amount of fromCurrency to targetCurrency with retry logic
	finalAmount, err := retryConversion(func() (float64, error) {
		return m.convert(req.Amount, fromCurrency, targetCurrency, apiCache)
	})
	if err != nil {
		// User-friendly error message
		errorMsg := fmt.Sprintf("Cannot convert %s to %s", fromCurrency, targetCurrency)
		if err.Error() != "" {
			errorMsg += ": " + err.Error()
		}
		log.Printf("Conversion error: %v", err)
		// Return nil to indicate no result rather than error
		return nil, 0, nil
	}

	if finalAmount < minAmountAfterFees {
		return nil, 0, fmt.Errorf("amount too small after fees: %f", finalAmount)
	}

	// Calculate display rate (effective rate including all fees)
	var displayRate float64
	if req.Amount > 0 {
		displayRate = finalAmount / req.Amount
	}

	if displayRate <= 0 || math.IsNaN(displayRate) || math.IsInf(displayRate, 0) {
		return nil, 0, fmt.Errorf("invalid display rate calculated")
	}

	// Check slippage for large orders with visible warning
	slippageInfo := ""
	if shouldUseOrderBook(req.Amount, fromCurrency, targetCurrency, apiCache) {
		var slippagePercent float64
		// Determine which symbol and direction to check for slippage
		if (fromCurrency == "TON" || getCurrencyType(fromCurrency, apiCache) == "crypto") &&
			(targetCurrency == "USDT" || getCurrencyType(targetCurrency, apiCache) == "crypto") {

			symbol := fromCurrency + "USDT"
			isBuy := false // selling fromCurrency
			if fromCurrency == "USDT" {
				symbol = targetCurrency + "USDT"
				isBuy = true // buying targetCurrency
			}

			// This is a simplification; the real check might involve intermediate steps.
			// For now, we check the primary trade leg.
			if slippage, err := apiCache.CalculateSlippage(symbol, req.Amount, isBuy); err == nil {
				slippagePercent = slippage
			}
		}

		if slippagePercent > slippageWarningThreshold {
			slippageInfo = fmt.Sprintf(" ⚠️ %.2f%% slippage", slippagePercent)
		}
	}

	return m.formatResult(req, targetCurrency, finalAmount, displayRate, baseScore, slippageInfo), finalAmount, nil
}

// formatInverseResult creates a display result for inverse conversions with correct market rates
func (m *CurrencyConverterModule) formatInverseResult(sourceAmount float64, sourceCurrency string, targetAmount float64, targetCurrency string, score int) *commontypes.FlowResult {
	var title, subTitle, clipboardText string

	// Calculate the market rate for display (how many source units per target unit)
	// For inverse results, we show how much source currency equals the target
	marketRate := sourceAmount / targetAmount

	var tag string
	var rateStr string

	hasRub := sourceCurrency == "RUB" || targetCurrency == "RUB"
	hasUsd := sourceCurrency == "USD" || sourceCurrency == "USDT" || targetCurrency == "USD" || targetCurrency == "USDT"

	if hasRub && hasUsd {
		if sourceCurrency == "USD" && targetCurrency == "RUB" {
			// Need X USD to buy Y RUB = buying RUB with USD = купить
			tag = " 🛍️ купить"
			// For USD->RUB inverse, show market rate as 1 RUB = X USD
			rateStr = fmt.Sprintf("1 RUB = %s USD", formatRate(1.0/marketRate))
		} else if sourceCurrency == "RUB" && targetCurrency == "USD" {
			// Need X RUB to get Y USD = selling RUB for USD = продать
			tag = " 🏷️ продать"
			// For RUB->USD inverse, show market rate as 1 USD = X RUB
			rateStr = fmt.Sprintf("1 USD = %s RUB", formatRate(marketRate))
		}
	} else {
		// For other currency pairs, show the market rate
		if marketRate > 0 && !math.IsNaN(marketRate) && !math.IsInf(marketRate, 0) {
			// Show in format: 1 TARGET = X SOURCE for inverse results
			rateStr = fmt.Sprintf("1 %s = %s %s", targetCurrency, formatRate(marketRate), sourceCurrency)
		}
	}

	// For clipboard: use formatted amount for readability
	clipboardText = formatAmountForClipboard(sourceAmount, sourceCurrency)

	// For display: use formatted amount
	formattedSourceAmount := formatAmount(sourceAmount, sourceCurrency)

	if m.ShortDisplayFormat {
		// Short format: show the source amount needed
		title = fmt.Sprintf("%s %s", formattedSourceAmount, sourceCurrency)
	} else {
		// Long format: show full inverse conversion
		formattedTargetAmount := formatAmount(targetAmount, targetCurrency)
		title = fmt.Sprintf("%s %s = %s %s",
			formattedSourceAmount, sourceCurrency,
			formattedTargetAmount, targetCurrency)
	}

	subTitle = rateStr + tag

	// Debug logging for verification
	log.Printf("Inverse: %s %s -> %s %s, Market Rate: %f, Display: %s",
		formattedSourceAmount, sourceCurrency,
		formatAmount(targetAmount, targetCurrency), targetCurrency,
		marketRate, rateStr)

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

// formatResult creates the display result with correct tags
func (m *CurrencyConverterModule) formatResult(req *ConversionRequest, targetCurrency string, finalAmount, displayRate float64, score int, slippageInfo string) *commontypes.FlowResult {
	var title, subTitle, clipboardText string

	// Correct buy/sell tags based on conversion direction
	var tag string

	hasRubFrom := req.FromCurrency == "RUB"
	hasRubTo := targetCurrency == "RUB"
	hasUsdFrom := req.FromCurrency == "USD" || req.FromCurrency == "USDT"
	hasUsdTo := targetCurrency == "USD" || targetCurrency == "USDT"

	if (hasRubFrom && hasUsdTo) || (hasUsdFrom && hasRubTo) {
		if hasRubFrom && hasUsdTo {
			// Converting RUB to USD = selling RUB for USD = продать
			tag = " 🏷️ продать"
		} else if hasUsdFrom && hasRubTo {
			// Converting USD to RUB = buying RUB with USD = купить
			tag = " 🛍️ купить"
		}
	}

	// For clipboard: use formatted amount for readability
	clipboardText = formatAmountForClipboard(finalAmount, targetCurrency)

	// For display: use formatted amount with thousand separators
	formattedConvertedAmount := formatAmount(finalAmount, targetCurrency)

	if m.ShortDisplayFormat {
		// Short format: just show the result
		title = fmt.Sprintf("%s %s", formattedConvertedAmount, targetCurrency)
	} else {
		// Long format: show full conversion
		formattedInputAmount := formatAmount(req.Amount, req.FromCurrency)
		title = fmt.Sprintf("%s %s = %s %s",
			formattedInputAmount, req.FromCurrency,
			formattedConvertedAmount, targetCurrency)
	}

	// Always show rate in format "1 USD = X RUB" for USD/RUB pairs
	var rateStr string
	if (hasRubFrom || hasRubTo) && (hasUsdFrom || hasUsdTo) {
		// For RUB/USD pairs, always show as 1 USD = X RUB
		if hasRubFrom && hasUsdTo {
			// RUB -> USD: calculate how many RUB per 1 USD based on effective rate
			if displayRate > 0 {
				rubPerUsd := 1.0 / displayRate
				rateStr = fmt.Sprintf("1 USD = %s RUB", formatRate(rubPerUsd))
			}
		} else if hasUsdFrom && hasRubTo {
			// USD -> RUB: displayRate is already RUB per USD
			rateStr = fmt.Sprintf("1 USD = %s RUB", formatRate(displayRate))
		}
	} else {
		// Standard rate display for other pairs
		rateStr = fmt.Sprintf("1 %s = %s %s", req.FromCurrency, formatRate(displayRate), targetCurrency)
	}

	subTitle = rateStr + tag + slippageInfo

	// Debug logging for verification
	log.Printf("Direct: %s %s -> %s %s, Display Rate: %f, Display: %s",
		formatAmount(req.Amount, req.FromCurrency), req.FromCurrency,
		formattedConvertedAmount, targetCurrency,
		displayRate, rateStr)

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
