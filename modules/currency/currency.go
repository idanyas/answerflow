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
	scoreReverseConversion  = 85 // Score for the reverse of a base conversion (e.g., RUB -> USD for a "usd" query)
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

// formatAmount formats a numeric amount based on its currency code's precision rules.
func formatAmount(amount float64, currencyCode string) string {
	precision, isHighPrecision := highPrecisionCurrencies[currencyCode]
	if !isHighPrecision {
		precision = 2
	}
	ac := accounting.Accounting{Symbol: "", Precision: precision}
	return ac.FormatMoneyFloat64(amount)
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

		res, _, errGen := m.generateConversionResult(ctx, parsedRequest, parsedRequest.ToCurrency, apiCache, scoreSpecificConversion, false)
		if errGen != nil {
			log.Printf("CurrencyConverterModule: Error generating specific conversion for %s to %s: %v", parsedRequest.FromCurrency, parsedRequest.ToCurrency, errGen)
		} else if res != nil {
			results = append(results, *res)
		}
	} else {
		// User did not specify a target currency, use defaults
		handledTargets := make(map[string]bool)
		var baseConversionSucceeded bool

		// 1. Perform Base Conversion first to get the amount for the reverse conversion.
		if m.baseConversionCurrency != "" &&
			m.baseConversionCurrency != parsedRequest.FromCurrency &&
			!handledTargets[m.baseConversionCurrency] {

			res, _, errGen := m.generateConversionResult(ctx, parsedRequest, m.baseConversionCurrency, apiCache, scoreBaseConversion, false)
			if errGen != nil {
				log.Printf("CurrencyConverterModule: Error generating base conversion for %s to %s: %v", parsedRequest.FromCurrency, m.baseConversionCurrency, errGen)
			} else if res != nil {
				results = append(results, *res)
				handledTargets[m.baseConversionCurrency] = true
				baseConversionSucceeded = true
			}
		}

		// 2. If Base Conversion was successful, generate the Reverse Conversion.
		if baseConversionSucceeded {
			// Create a request that asks: "How much of the base currency (e.g., RUB)
			// is required to get the user's original amount and currency (e.g., 24 USD)?"
			reverseRequest := &ConversionRequest{
				Amount:       parsedRequest.Amount,       // The target amount (e.g., 24)
				FromCurrency: m.baseConversionCurrency,   // The currency we are paying with (e.g., RUB)
				ToCurrency:   parsedRequest.FromCurrency, // The currency we want to receive (e.g., USDT)
			}

			res, _, errGen := m.generateConversionResult(ctx, reverseRequest, reverseRequest.ToCurrency, apiCache, scoreReverseConversion, true)
			if errGen != nil {
				log.Printf("CurrencyConverterModule: Error generating reverse conversion for %s to %s: %v", reverseRequest.FromCurrency, reverseRequest.ToCurrency, errGen)
			} else if res != nil {
				results = append(results, *res)
			}
		}

		// 3. Quick conversions
		for _, target := range m.quickConversionTargets {
			if target == parsedRequest.FromCurrency || handledTargets[target] {
				continue
			}
			res, _, errGen := m.generateConversionResult(ctx, parsedRequest, target, apiCache, scoreQuickConversion, false)
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
// If isReverse is true, it calculates the required amount of `req.FromCurrency` to get `req.Amount` of `targetCurrency`.
// If isReverse is false, it calculates the converted amount from `req.Amount` of `req.FromCurrency` to `targetCurrency`.
func (m *CurrencyConverterModule) generateConversionResult(
	ctx context.Context,
	req *ConversionRequest,
	targetCurrency string,
	apiCache *APICache,
	baseScore int,
	isReverse bool) (*commontypes.FlowResult, float64, error) {

	// ALIASING: Handle USD as an alias for USDT for the target currency.
	effectiveTargetCurrency := targetCurrency
	if effectiveTargetCurrency == "USD" {
		effectiveTargetCurrency = "USDT"
	}

	// The req.FromCurrency is already aliased in ProcessQuery.
	fromCurrency := req.FromCurrency
	if fromCurrency == effectiveTargetCurrency {
		return nil, 0, nil
	}

	var finalAmount float64 // For direct: converted amount. For reverse: required amount.
	var displayRate float64
	var err error
	sourceName := "currency-api"
	score := baseScore

	// --- Determine provider and calculate ---
	useWhitebird := true
	switch {
	case fromCurrency == "RUB" && effectiveTargetCurrency == "USDT":
	case fromCurrency == "USDT" && effectiveTargetCurrency == "RUB":
	case fromCurrency == "BYN" && effectiveTargetCurrency == "USDT":
		// We don't have a USDT->BYN formula, so this pair is one-way for Whitebird.
		if isReverse {
			useWhitebird = false
		}
	default:
		useWhitebird = false
	}

	if useWhitebird {
		sourceName = "Blackanimal"
		// Only boost score for direct special conversions, not reverse ones.
		if !isReverse {
			score = scoreWhitebird
		}

		if isReverse {
			// REVERSE LOGIC: Calculate required `fromCurrency` to get `req.Amount` of `effectiveTargetCurrency`
			targetAmount := req.Amount
			switch {
			case fromCurrency == "RUB" && effectiveTargetCurrency == "USDT": // How many RUB to get X USDT?
				rawRate, errGet := apiCache.GetWhitebirdRate("RUB", "USDT")
				if errGet == nil {
					finalAmount = (rawRate * (targetAmount*1.0217 + 0.038541)) / 0.97561
				}
				err = errGet
			case fromCurrency == "USDT" && effectiveTargetCurrency == "RUB": // How many USDT to get X RUB?
				rawRate, errGet := apiCache.GetWhitebirdRate("USDT", "RUB")
				if errGet == nil {
					finalAmount = (targetAmount * 1.015) / (rawRate * 0.985)
				}
				err = errGet
			}
			if targetAmount > 0 {
				displayRate = finalAmount / targetAmount
			}
		} else {
			// DIRECT LOGIC
			initialAmount := req.Amount
			switch {
			case fromCurrency == "RUB" && effectiveTargetCurrency == "USDT":
				rawRate, errGet := apiCache.GetWhitebirdRate("RUB", "USDT")
				if errGet == nil {
					fiatFee := initialAmount * 0.02439
					cryptoFee := 0.038541 * rawRate
					netToConvert := initialAmount - fiatFee - cryptoFee
					if netToConvert > 0 {
						converted := netToConvert / rawRate
						finalAmount = converted / 1.0217
					}
				}
				err = errGet
			case fromCurrency == "USDT" && effectiveTargetCurrency == "RUB":
				rawRate, errGet := apiCache.GetWhitebirdRate("USDT", "RUB")
				if errGet == nil {
					converted := (initialAmount * rawRate) * 0.985
					finalAmount = converted / 1.015
				}
				err = errGet
			case fromCurrency == "BYN" && effectiveTargetCurrency == "USDT":
				rawRate, errGet := apiCache.GetWhitebirdRate("BYN", "USDT")
				if errGet == nil {
					fiatFee := initialAmount * 0.024371
					cryptoFee := 0.038778 * rawRate
					netToConvert := initialAmount - fiatFee - cryptoFee
					if netToConvert > 0 {
						finalAmount = netToConvert / rawRate
					}
				}
				err = errGet
			}
			if initialAmount > 0 {
				displayRate = finalAmount / initialAmount
			}
		}

		if err != nil {
			return nil, 0, fmt.Errorf("getting Whitebird rate: %w", err)
		}

	} else {
		// --- Default Provider Fallback ---
		if isReverse {
			targetAmount := req.Amount
			rate, _, errGet := apiCache.GetConversionRate(ctx, fromCurrency, effectiveTargetCurrency)
			if errGet != nil {
				return nil, 0, fmt.Errorf("getting reverse conversion rate: %w", errGet)
			}
			if rate > 0 {
				finalAmount = targetAmount / rate
				displayRate = 1 / rate // The effective rate is for target -> from
			}
			err = errGet
		} else {
			initialAmount := req.Amount
			rate, _, errGet := apiCache.GetConversionRate(ctx, fromCurrency, effectiveTargetCurrency)
			if errGet != nil {
				return nil, 0, fmt.Errorf("getting conversion rate: %w", errGet)
			}
			finalAmount = initialAmount * rate
			displayRate = rate
			err = errGet
		}
	}

	if err != nil {
		return nil, 0, fmt.Errorf("failed during rate calculation: %w", err)
	}

	finalAmount = math.Max(0, finalAmount)
	return m.formatResult(req, targetCurrency, finalAmount, displayRate, score, sourceName, isReverse), finalAmount, nil
}

// formatResult formats the final result into a FlowResult.
func (m *CurrencyConverterModule) formatResult(
	req *ConversionRequest,
	targetCurrency string,
	finalAmount float64,
	displayRate float64,
	score int,
	sourceName string,
	isReverse bool) *commontypes.FlowResult {

	var title, subTitle, clipboardText string

	if isReverse {
		// --- REVERSE CONVERSION FORMATTING ---
		// Here, `finalAmount` is the required amount of `req.FromCurrency`.
		// `req.Amount` is the target amount of `targetCurrency`.

		requiredAmount := finalAmount
		requiredCurrency := req.FromCurrency
		formattedRequiredAmount := formatAmount(requiredAmount, requiredCurrency)
		title = fmt.Sprintf("%s %s", formattedRequiredAmount, requiredCurrency)

		// targetAmount := req.Amount
		// formattedTargetAmount := formatAmount(targetAmount, targetCurrency)
		// subTitle = fmt.Sprintf("Amount in %s required to receive %s %s", requiredCurrency, formattedTargetAmount, targetCurrency)
		subTitle = fmt.Sprintf("1 %s = %s %s 🛒", req.FromCurrency, formatRate(displayRate), targetCurrency)

		outputPrecision, isHighPrecision := highPrecisionCurrencies[requiredCurrency]
		if !isHighPrecision {
			outputPrecision = 2
		}
		clipboardText = strconv.FormatFloat(requiredAmount, 'f', outputPrecision, 64)

	} else {
		// --- DIRECT CONVERSION FORMATTING ---
		// Here, `finalAmount` is the converted amount in `targetCurrency`.
		// `req.Amount` is the initial amount in `req.FromCurrency`.

		formattedInputAmount := formatAmount(req.Amount, req.FromCurrency)
		outputPrecision, isOutputHighPrecision := highPrecisionCurrencies[targetCurrency]
		if !isOutputHighPrecision {
			outputPrecision = 2
		}
		acOutput := accounting.Accounting{Symbol: "", Precision: outputPrecision}
		formattedConvertedAmount := acOutput.FormatMoneyFloat64(finalAmount)

		if m.ShortDisplayFormat {
			title = fmt.Sprintf("%s %s", formattedConvertedAmount, targetCurrency)
		} else {
			title = fmt.Sprintf("%s %s = %s %s",
				formattedInputAmount, req.FromCurrency,
				formattedConvertedAmount, targetCurrency)
		}

		var rateStr string
		lookupKey := fmt.Sprintf("%s_%s", req.FromCurrency, targetCurrency)
		if _, shouldInvert := m.invertedRatePairs[lookupKey]; shouldInvert && displayRate > 0 {
			invertedRate := 1 / displayRate
			rateStr = fmt.Sprintf("1 %s = %s %s", targetCurrency, formatRate(invertedRate), req.FromCurrency)
		} else {
			rateStr = fmt.Sprintf("1 %s = %s %s", req.FromCurrency, formatRate(displayRate), targetCurrency)
		}

		// if m.ShortDisplayFormat {
		// inputStr := fmt.Sprintf("%s %s", formattedInputAmount, req.FromCurrency)
		// subTitle = fmt.Sprintf("%s  ·  %s", inputStr, rateStr)
		// } else {
		subTitle = rateStr
		// }

		clipboardText = strconv.FormatFloat(finalAmount, 'f', outputPrecision, 64)
	}

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
	if rate <= 0 {
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
