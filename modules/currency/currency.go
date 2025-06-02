package currency // Stays as package currency

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"answerflow/commontypes"

	"github.com/leekchan/accounting"
)

const (
	scoreSpecificConversion = 100
	scoreBaseConversion     = 90
	scoreQuickConversion    = 80
	scoreP2PConversion      = 110 // Higher score for P2P direct match
)

// CurrencyConverterModule handles currency conversion queries.
type CurrencyConverterModule struct {
	quickConversionTargets []string
	baseConversionCurrency string
	defaultIconPath        string
	currencyData           *CurrencyData
	ShortDisplayFormat     bool // true for "Output only", false for "Input = Output"
}

// BybitP2PItemDetailsForSubtitle holds specific details from a P2P offer for display.
type BybitP2PItemDetailsForSubtitle struct {
	NickName  string
	MinAmount string // Original string format for display
	MaxAmount string // Original string format for display
}

// NewCurrencyConverterModule creates a new instance of the currency converter.
func NewCurrencyConverterModule(quickTargets []string, baseCurrency, iconPath string, shortDisplay bool) *CurrencyConverterModule {
	normalizedQuickTargets := make([]string, len(quickTargets))
	for i, target := range quickTargets {
		normalizedQuickTargets[i] = strings.ToUpper(target)
	}

	return &CurrencyConverterModule{
		quickConversionTargets: normalizedQuickTargets,
		baseConversionCurrency: strings.ToUpper(baseCurrency),
		defaultIconPath:        iconPath,
		currencyData:           NewCurrencyData(),
		ShortDisplayFormat:     shortDisplay,
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

	allCurrencies, err := apiCache.GetAllCurrencies(ctx)
	if err != nil {
		log.Printf("Warning: CurrencyConverterModule: failed to load all currency definitions for dynamic aliases: %v", err)
	}
	if allCurrencies != nil || !m.currencyData.initialised {
		m.currencyData.PopulateDynamicAliases(allCurrencies)
	}

	parsedRequest, err := ParseQuery(query, m.currencyData)
	if err != nil {
		return nil, nil // No error returned to main, just no results for this module
	}

	var results []commontypes.FlowResult
	ac := accounting.Accounting{Precision: 2}

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

		res, errGen := m.generateConversionResult(ctx, parsedRequest, parsedRequest.ToCurrency, apiCache, scoreSpecificConversion, ac)
		if errGen != nil {
			log.Printf("CurrencyConverterModule: Error generating specific conversion for %s to %s: %v", parsedRequest.FromCurrency, parsedRequest.ToCurrency, errGen)
			// Do not return error, just skip this result
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
			res, errGen := m.generateConversionResult(ctx, parsedRequest, m.baseConversionCurrency, apiCache, scoreBaseConversion, ac)
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
			res, errGen := m.generateConversionResult(ctx, parsedRequest, target, apiCache, scoreQuickConversion, ac)
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
	baseScore int, // Use baseScore, P2P might override it
	ac accounting.Accounting) (*commontypes.FlowResult, error) {

	if req.FromCurrency == targetCurrency {
		return nil, fmt.Errorf("cannot convert currency %s to itself", req.FromCurrency)
	}

	var rate float64
	var rateDate string // Only populated by standard API for now
	var err error
	currentScore := baseScore
	sourceName := "API"
	var p2pDetails *BybitP2PItemDetailsForSubtitle

	// --- Bybit P2P Integration ---
	// Determine if this is a USDT/RUB or USD/RUB pair
	isP2PCandidate := false
	bybitSide := "" // "0" for SELL USDT, "1" for BUY USDT

	// Normalize FromCurrency to USDT if it's USD for Bybit context
	normalizedFromCurrencyForP2P := req.FromCurrency
	if normalizedFromCurrencyForP2P == "USD" {
		normalizedFromCurrencyForP2P = "USDT"
	}
	// Normalize TargetCurrency to USDT if it's USD for Bybit context
	normalizedTargetCurrencyForP2P := targetCurrency
	if normalizedTargetCurrencyForP2P == "USD" {
		normalizedTargetCurrencyForP2P = "USDT"
	}

	if normalizedFromCurrencyForP2P == "USDT" && normalizedTargetCurrencyForP2P == "RUB" {
		isP2PCandidate = true
		bybitSide = "0" // User sells USDT to get RUB (Bybit Ad: Buyer wants USDT, offers RUB) -> Highest price
	} else if normalizedFromCurrencyForP2P == "RUB" && normalizedTargetCurrencyForP2P == "USDT" {
		isP2PCandidate = true
		bybitSide = "1" // User sells RUB to get USDT (Bybit Ad: Seller has USDT, wants RUB) -> Lowest price
	}

	if isP2PCandidate {
		bybitOffer, bybitErr := apiCache.GetBybitP2PBestOffer(ctx, bybitSide)
		if bybitErr == nil && bybitOffer != nil && bybitOffer.PriceFloat > 0 {
			p2pPrice := bybitOffer.PriceFloat // This is always RUB per USDT from Bybit offers

			if normalizedFromCurrencyForP2P == "USDT" { // User converting USDT to RUB
				rate = p2pPrice // Rate is RUB per USDT
			} else { // User converting RUB to USDT
				rate = 1.0 / p2pPrice // Rate is USDT per RUB
			}
			sourceName = "Bybit P2P"
			currentScore = scoreP2PConversion // Boost score for P2P
			p2pDetails = &BybitP2PItemDetailsForSubtitle{
				NickName:  bybitOffer.NickName,
				MinAmount: bybitOffer.MinAmount,
				MaxAmount: bybitOffer.MaxAmount,
			}
			// No specific date for P2P rates, they are live
		} else {
			if bybitErr != nil {
				log.Printf("CurrencyConverterModule: Bybit P2P fetch failed for %s (%s) to %s (%s), side %s: %v. Falling back.",
					req.FromCurrency, normalizedFromCurrencyForP2P,
					targetCurrency, normalizedTargetCurrencyForP2P,
					bybitSide, bybitErr)
			} else if bybitOffer == nil || bybitOffer.PriceFloat <= 0 {
				log.Printf("CurrencyConverterModule: No valid Bybit P2P offer or zero price for %s (%s) to %s (%s), side %s. Falling back.",
					req.FromCurrency, normalizedFromCurrencyForP2P,
					targetCurrency, normalizedTargetCurrencyForP2P,
					bybitSide)
			}
			// Fallback to standard API will occur as sourceName is still "API"
		}
	}
	// --- End Bybit P2P Integration ---

	if sourceName == "API" { // If not P2P or P2P failed
		rate, rateDate, err = apiCache.GetConversionRate(ctx, req.FromCurrency, targetCurrency)
		if err != nil {
			return nil, fmt.Errorf("getting conversion rate for %s to %s from standard API: %w", req.FromCurrency, targetCurrency, err)
		}
		if rate == 0 {
			log.Printf("Warning: CurrencyConverterModule: Zero rate from standard API for %s to %s.", req.FromCurrency, targetCurrency)
			// Allow zero rate to proceed, might be a valid (though unlikely) scenario or an API issue.
		}
	}

	convertedAmount := req.Amount * rate

	formattedInputAmount := ac.FormatMoneyFloat64(req.Amount)
	formattedConvertedAmount := ac.FormatMoneyFloat64(convertedAmount)

	var title string
	if m.ShortDisplayFormat {
		title = fmt.Sprintf("%s %s", formattedConvertedAmount, targetCurrency)
	} else {
		title = fmt.Sprintf("%s %s = %s %s",
			formattedInputAmount, req.FromCurrency,
			formattedConvertedAmount, targetCurrency)
	}

	subTitleBase := fmt.Sprintf("1 %s = %s %s", req.FromCurrency, formatRate(rate), targetCurrency)
	var subTitleExtra string
	if sourceName == "Bybit P2P" && p2pDetails != nil {
		// subTitleExtra = fmt.Sprintf(" (Bybit P2P: @%s, Avail: %s-%s %s)", p2pDetails.NickName, p2pDetails.MinAmount, p2pDetails.MaxAmount, bybitP2PFixedCurrencyID)
	} else if sourceName == "API" && rateDate != "" {
		// subTitleExtra = fmt.Sprintf(" (Rate from %s)", rateDate) // Original format with date
		// Subtitle format was changed to not include date for standard API, keeping it concise.
		// If date is desired for standard API, uncomment above and adjust.
	}
	subTitle := subTitleBase + subTitleExtra

	clipboardText := formattedConvertedAmount
	if ac.Thousand != "" {
		clipboardText = strings.ReplaceAll(formattedConvertedAmount, ac.Thousand, "")
	}

	return &commontypes.FlowResult{
		Title:    title,
		SubTitle: subTitle,
		Score:    currentScore,
		JsonRPCAction: commontypes.JsonRPCAction{
			Method:     "copy_to_clipboard",
			Parameters: []interface{}{clipboardText},
		},
	}, nil
}

// formatRate formats the exchange rate with appropriate precision.
func formatRate(rate float64) string {
	if rate == 0 {
		return "0"
	}
	var formattedRate string
	if rate < 0.00000001 && rate > 0 { // Very small non-zero rates (e.g. for crypto dust)
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
