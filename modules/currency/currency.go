package currency

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	"answerflow/commontypes"

	"github.com/leekchan/accounting"
)

const (
	scoreSpecificConversion = 100
	scoreBaseConversion     = 90
	scoreQuickConversion    = 80
	scoreWhitebird          = 110 // Higher score for Whitebird direct match
)

// highPrecisionCurrencies defines currencies that need more than 2 decimal places for clipboard operations.
var highPrecisionCurrencies = map[string]int{
	"BTC": 8,
	"ETH": 8,
}

// CurrencyConverterModule handles currency conversion queries.
type CurrencyConverterModule struct {
	quickConversionTargets []string
	baseConversionCurrency string
	defaultIconPath        string
	currencyData           *CurrencyData
	ShortDisplayFormat     bool // true for "Output only", false for "Input = Output"
	invertedRatePairs      map[string]bool
}

// NewCurrencyConverterModule creates a new instance of the currency converter.
func NewCurrencyConverterModule(quickTargets []string, baseCurrency, iconPath string, shortDisplay bool) *CurrencyConverterModule {
	normalizedQuickTargets := make([]string, len(quickTargets))
	for i, target := range quickTargets {
		normalizedQuickTargets[i] = strings.ToUpper(target)
	}

	// Define the pairs for which the subtitle rate should be inverted.
	// Key is in "FROM_TO" format.
	invertedPairs := map[string]bool{
		"RUB_USD":  true,
		"RUB_EUR":  true,
		"RUB_USDT": true, // Also handle the aliased USDT case
	}

	return &CurrencyConverterModule{
		quickConversionTargets: normalizedQuickTargets,
		baseConversionCurrency: strings.ToUpper(baseCurrency),
		defaultIconPath:        iconPath,
		currencyData:           NewCurrencyData(),
		ShortDisplayFormat:     shortDisplay,
		invertedRatePairs:      invertedPairs,
	}
}

// Name returns the name of the module.
func (m *CurrencyConverterModule) Name() string {
	return "CurrencyConverter"
}

// DefaultIconPath returns the default icon for this module.
func (m *CurrencyConverterModule) DefaultIconPath() string {
	return m.defaultIconPath
}

// ProcessQuery handles a user's query.
func (m *CurrencyConverterModule) ProcessQuery(ctx context.Context, query string, apiCache *APICache) ([]commontypes.FlowResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	// Populate dynamic aliases from the cache if not already done.
	if !m.currencyData.initialised {
		allCurrencies, err := apiCache.GetAllCurrencies()
		if err != nil {
			log.Printf("Warning: CurrencyConverterModule: failed to load all currency definitions from cache: %v", err)
		} else {
			m.currencyData.PopulateDynamicAliases(allCurrencies)
		}
	}

	parsedRequest, err := ParseQuery(query, m.currencyData)
	if err != nil {
		return nil, nil // No error returned to main, just no results for this module
	}

	// --- USD to USDT Alias Logic ---
	if parsedRequest.FromCurrency == "USD" {
		parsedRequest.FromCurrency = "USDT"
	}
	if parsedRequest.ToCurrency == "USD" {
		parsedRequest.ToCurrency = "USDT"
	}
	// --- End Alias Logic ---

	var results []commontypes.FlowResult

	if parsedRequest.ToCurrency != "" {
		// User specified a target currency
		toCurrencyCanonical, resolveErr := m.currencyData.ResolveCurrency(parsedRequest.ToCurrency)
		if resolveErr != nil {
			log.Printf("CurrencyConverterModule: could not resolve ToCurrency '%s': %v", parsedRequest.ToCurrency, resolveErr)
			return nil, nil
		}
		parsedRequest.ToCurrency = toCurrencyCanonical

		if parsedRequest.FromCurrency == parsedRequest.ToCurrency {
			return nil, nil // Avoid converting to itself
		}

		res, errGen := m.generateConversionResult(ctx, parsedRequest, parsedRequest.ToCurrency, apiCache, scoreSpecificConversion)
		if errGen != nil {
			log.Printf("CurrencyConverterModule: Error generating specific conversion for %s to %s: %v", parsedRequest.FromCurrency, parsedRequest.ToCurrency, errGen)
		} else if res != nil {
			results = append(results, *res)
		}
	} else {
		// User did not specify a target currency, use defaults
		handledTargets := make(map[string]bool)

		// Base conversion
		if m.baseConversionCurrency != "" &&
			m.baseConversionCurrency != parsedRequest.FromCurrency &&
			!handledTargets[m.baseConversionCurrency] {
			res, errGen := m.generateConversionResult(ctx, parsedRequest, m.baseConversionCurrency, apiCache, scoreBaseConversion)
			if errGen != nil {
				log.Printf("CurrencyConverterModule: Error generating base conversion for %s to %s: %v", parsedRequest.FromCurrency, m.baseConversionCurrency, errGen)
			} else if res != nil {
				results = append(results, *res)
				handledTargets[m.baseConversionCurrency] = true
			}
		}

		// Quick conversions
		for _, target := range m.quickConversionTargets {
			if target == parsedRequest.FromCurrency || handledTargets[target] {
				continue
			}
			res, errGen := m.generateConversionResult(ctx, parsedRequest, target, apiCache, scoreQuickConversion)
			if errGen != nil {
				log.Printf("CurrencyConverterModule: Error generating quick conversion for %s to %s: %v", parsedRequest.FromCurrency, target, errGen)
				continue
			}
			if res != nil {
				results = append(results, *res)
				handledTargets[target] = true
			}
		}
	}

	if len(results) == 0 {
		return nil, nil
	}
	return results, nil
}

// generateConversionResult creates a FlowResult for a given conversion.
func (m *CurrencyConverterModule) generateConversionResult(
	ctx context.Context,
	req *ConversionRequest,
	targetCurrency string,
	apiCache *APICache,
	baseScore int) (*commontypes.FlowResult, error) {

	// ALIASING: Handle USD as an alias for USDT for the target currency.
	effectiveTargetCurrency := targetCurrency
	if effectiveTargetCurrency == "USD" {
		effectiveTargetCurrency = "USDT"
	}

	// The req.FromCurrency is already aliased in ProcessQuery.
	// This check now correctly prevents USDT -> USDT self-conversion (e.g. for a "1 usd" query).
	if req.FromCurrency == effectiveTargetCurrency {
		return nil, nil
	}

	// --- Whitebird Provider Logic ---
	isWhitebirdPair := true
	var finalAmount float64
	var effectiveRate float64
	var rawRate float64
	var err error

	// Use the aliased effectiveTargetCurrency for logic checks.
	switch {
	case req.FromCurrency == "RUB" && effectiveTargetCurrency == "USDT":
		rawRate, err = apiCache.GetWhitebirdRate("RUB", "USDT")
		if err == nil {
			fiatFee := req.Amount * 0.02439
			cryptoFee := 0.038541 * rawRate
			netToConvert := req.Amount - fiatFee - cryptoFee
			// MODIFIED: Prevent negative conversion results if fees exceed the amount.
			if netToConvert <= 0 {
				finalAmount = 0
			} else {
				converted := netToConvert / rawRate
				finalAmount = converted / 1.0217 // Add +2.17% fee (Sber Pay)
			}
		}

	case req.FromCurrency == "USDT" && effectiveTargetCurrency == "RUB":
		rawRate, err = apiCache.GetWhitebirdRate("USDT", "RUB")
		if err == nil {
			converted := (req.Amount * rawRate) * 0.985
			finalAmount = converted / 1.015 // Add +1.5% fee (MIR cards payout)
		}

	case req.FromCurrency == "BYN" && effectiveTargetCurrency == "USDT":
		rawRate, err = apiCache.GetWhitebirdRate("BYN", "USDT")
		if err == nil {
			fiatFee := req.Amount * 0.024371
			cryptoFee := 0.038778 * rawRate
			netToConvert := req.Amount - fiatFee - cryptoFee
			// MODIFIED: Prevent negative conversion results if fees exceed the amount.
			if netToConvert <= 0 {
				finalAmount = 0
			} else {
				finalAmount = netToConvert / rawRate
			}
		}

	default:
		isWhitebirdPair = false
	}

	if isWhitebirdPair {
		if err != nil {
			return nil, fmt.Errorf("getting Whitebird rate: %w", err)
		}
		// Ensure final amount is not negative.
		finalAmount = math.Max(0, finalAmount)
		if req.Amount > 0 {
			effectiveRate = finalAmount / req.Amount
		}
		// Pass the original targetCurrency for display purposes.
		return m.formatResult(req, targetCurrency, finalAmount, effectiveRate, scoreWhitebird, "Blackanimal"), nil
	}
	// --- End Whitebird Provider Logic ---

	// --- Default Provider Fallback ---
	// Use the aliased effectiveTargetCurrency for fetching the rate.
	rate, _, err := apiCache.GetConversionRate(ctx, req.FromCurrency, effectiveTargetCurrency)
	if err != nil {
		return nil, fmt.Errorf("getting conversion rate from default provider: %w", err)
	}
	convertedAmount := req.Amount * rate
	// Pass the original targetCurrency for display purposes.
	return m.formatResult(req, targetCurrency, convertedAmount, rate, baseScore, "currency-api"), nil
}

// formatResult formats the final result into a FlowResult.
func (m *CurrencyConverterModule) formatResult(
	req *ConversionRequest,
	targetCurrency string,
	finalAmount float64,
	displayRate float64,
	score int,
	sourceName string) *commontypes.FlowResult {

	// Determine precision for the input currency
	inputPrecision, isInputHighPrecision := highPrecisionCurrencies[req.FromCurrency]
	if !isInputHighPrecision {
		inputPrecision = 2
	}
	acInput := accounting.Accounting{Symbol: "", Precision: inputPrecision}
	formattedInputAmount := acInput.FormatMoneyFloat64(req.Amount)

	// Determine precision for the output currency (target)
	outputPrecision, isOutputHighPrecision := highPrecisionCurrencies[targetCurrency]
	if !isOutputHighPrecision {
		outputPrecision = 2
	}
	acOutput := accounting.Accounting{Symbol: "", Precision: outputPrecision}
	formattedConvertedAmount := acOutput.FormatMoneyFloat64(finalAmount)

	var title string
	if m.ShortDisplayFormat {
		title = fmt.Sprintf("%s %s", formattedConvertedAmount, targetCurrency)
	} else {
		title = fmt.Sprintf("%s %s = %s %s",
			formattedInputAmount, req.FromCurrency,
			formattedConvertedAmount, targetCurrency)
	}

	// Invert subtitle for specific pairs for better readability
	var subTitle string
	lookupKey := fmt.Sprintf("%s_%s", req.FromCurrency, targetCurrency)
	if _, shouldInvert := m.invertedRatePairs[lookupKey]; shouldInvert && displayRate > 0 {
		invertedRate := 1 / displayRate
		// subTitle = fmt.Sprintf("1 %s = %s %s · %s", targetCurrency, formatRate(invertedRate), req.FromCurrency, sourceName)
		subTitle = fmt.Sprintf("1 %s = %s %s", targetCurrency, formatRate(invertedRate), req.FromCurrency)
	} else {
		subTitle = fmt.Sprintf("1 %s = %s %s", req.FromCurrency, formatRate(displayRate), targetCurrency)
	}

	if sourceName == "Whitebird" {
		// subTitle = fmt.Sprintf("Effective rate via %s: 1 %s ≈ %s %s", sourceName, req.FromCurrency, formatRate(displayRate), targetCurrency)
	}

	// Use the determined outputPrecision for the clipboard text.
	clipboardText := strconv.FormatFloat(finalAmount, 'f', outputPrecision, 64)

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

// formatRate formats the exchange rate with appropriate precision.
func formatRate(rate float64) string {
	if rate <= 0 { // MODIFIED: handle zero or negative rates cleanly
		return "0"
	}
	var formattedRate string
	if rate < 0.00000001 {
		formattedRate = strconv.FormatFloat(rate, 'f', 10, 64)
	} else if rate < 0.0001 {
		formattedRate = strconv.FormatFloat(rate, 'f', 8, 64)
	} else if rate < 0.01 {
		formattedRate = strconv.FormatFloat(rate, 'f', 6, 64)
	} else if rate < 1 {
		formattedRate = strconv.FormatFloat(rate, 'f', 4, 64)
	} else if rate < 1000 {
		formattedRate = strconv.FormatFloat(rate, 'f', 4, 64)
	} else {
		formattedRate = strconv.FormatFloat(rate, 'f', 2, 64)
	}
	formattedRate = strings.TrimRight(formattedRate, "0")
	formattedRate = strings.TrimRight(formattedRate, ".")
	if formattedRate == "" || formattedRate == "." {
		return "0"
	}
	return formattedRate
}
