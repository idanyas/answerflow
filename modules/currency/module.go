package currency

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"answerflow/commontypes"
)

type CurrencyConverterModule struct {
	quickConversionTargets []string
	baseConversionCurrency string
	defaultIconPath        string
	currencyData           *CurrencyData
	ShortDisplayFormat     bool
}

func NewCurrencyConverterModule(quickTargets []string, baseCurrency, iconPath string, shortDisplay bool) *CurrencyConverterModule {
	normalizedQuickTargets := make([]string, len(quickTargets))
	for i, target := range quickTargets {
		normalizedQuickTargets[i] = strings.ToUpper(target)
	}

	currencyData := NewCurrencyData()
	apiCurrencies := make(map[string]string)
	// Add known cryptos
	for _, crypto := range supportedCryptos {
		apiCurrencies[crypto] = crypto + " Cryptocurrency"
	}
	// Add known fiats
	for _, fiat := range supportedFiats {
		apiCurrencies[fiat] = fiat + " Currency"
	}
	currencyData.PopulateDynamicAliases(apiCurrencies)

	return &CurrencyConverterModule{
		quickConversionTargets: normalizedQuickTargets,
		baseConversionCurrency: strings.ToUpper(baseCurrency),
		defaultIconPath:        iconPath,
		currencyData:           currencyData,
		ShortDisplayFormat:     shortDisplay,
	}
}

func (m *CurrencyConverterModule) Name() string {
	return "CurrencyConverter"
}

func (m *CurrencyConverterModule) DefaultIconPath() string {
	return m.defaultIconPath
}

// Global flag to prevent concurrent cache refreshes
var cacheRefreshInProgress atomic.Bool

func (m *CurrencyConverterModule) ProcessQuery(ctx context.Context, query string, apiCache *APICache) ([]commontypes.FlowResult, error) {
	// Validate apiCache is not nil
	if apiCache == nil {
		return nil, fmt.Errorf("API cache is not initialized")
	}

	// Check stale data and log warning (don't wait for refresh)
	if apiCache.IsStale() {
		staleness := apiCache.GetCacheStaleness()
		for provider, duration := range staleness {
			if duration > time.Hour*4 {
				log.Printf("WARNING: %s data is critically stale (%v old), results may be inaccurate", provider, duration)
			}
		}
		// Launch refresh in background without waiting, but only if not already running
		if cacheRefreshInProgress.CompareAndSwap(false, true) {
			go func() {
				defer cacheRefreshInProgress.Store(false)
				if err := apiCache.ForceRefresh(); err != nil {
					log.Printf("Failed to refresh stale cache: %v", err)
				}
			}()
		}
	}

	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	parsedRequest, err := ParseQuery(query, m.currencyData)
	if err != nil {
		return nil, nil
	}

	// Validate parsed amount including negative check
	if parsedRequest.Amount <= 0 || parsedRequest.Amount > maxConversionAmount ||
		math.IsNaN(parsedRequest.Amount) || math.IsInf(parsedRequest.Amount, 0) {
		log.Printf("Invalid amount in query: %f", parsedRequest.Amount)
		return nil, nil
	}

	var results []commontypes.FlowResult

	if parsedRequest.ToCurrency != "" {
		// Specific conversion requested
		toCurrencyCanonical, resolveErr := m.currencyData.ResolveCurrency(parsedRequest.ToCurrency)
		if resolveErr != nil {
			log.Printf("CurrencyConverterModule: could not resolve ToCurrency '%s': %v", parsedRequest.ToCurrency, resolveErr)
			return nil, nil
		}
		parsedRequest.ToCurrency = toCurrencyCanonical

		// Check for same currency conversion
		if parsedRequest.FromCurrency == parsedRequest.ToCurrency {
			// Return a message indicating same currency
			result := commontypes.FlowResult{
				Title:    fmt.Sprintf("%s %s", formatAmount(parsedRequest.Amount, parsedRequest.FromCurrency), parsedRequest.FromCurrency),
				SubTitle: "Same currency - no conversion needed",
				Score:    100,
				JsonRPCAction: commontypes.JsonRPCAction{
					Method:     "copy_to_clipboard",
					Parameters: []interface{}{formatAmountForClipboard(parsedRequest.Amount, parsedRequest.FromCurrency)},
				},
			}
			return []commontypes.FlowResult{result}, nil
		}

		res, _, errGen := m.generateConversionResult(parsedRequest, parsedRequest.ToCurrency, apiCache, scoreSpecificConversion)
		if errGen != nil {
			log.Printf("CurrencyConverterModule: Error generating conversion %s to %s: %v", parsedRequest.FromCurrency, parsedRequest.ToCurrency, errGen)
		} else if res != nil {
			results = append(results, *res)
		}
	} else {
		// No target specified - use defaults with enhanced EUR handling
		switch parsedRequest.FromCurrency {
		case "RUB":
			// 1. RUB -> USD (selling RUB for USD)
			res, _, err := m.generateConversionResult(parsedRequest, "USD", apiCache, scoreBaseConversion)
			if err == nil && res != nil {
				results = append(results, *res)
			}

			// 2. How much USD needed to buy X RUB (buying RUB with USD) - INVERSE
			usdAmount, err := m.findInverseAmount(parsedRequest.Amount, "USD", "RUB", apiCache)
			if err == nil && usdAmount > 0 {
				res := m.formatInverseResult(usdAmount, "USD", parsedRequest.Amount, "RUB", scoreReverseConversion)
				if res != nil {
					results = append(results, *res)
				}
			}

			// 3. RUB -> EUR
			res, _, err = m.generateConversionResult(parsedRequest, "EUR", apiCache, scoreQuickConversion)
			if err == nil && res != nil {
				results = append(results, *res)
			}

		case "USD":
			// 1. USD -> RUB (buying RUB with USD)
			res, _, err := m.generateConversionResult(parsedRequest, "RUB", apiCache, scoreBaseConversion)
			if err == nil && res != nil {
				results = append(results, *res)
			}

			// 2. How much RUB needed to get X USD (selling RUB for USD) - INVERSE
			rubAmount, err := m.findInverseAmount(parsedRequest.Amount, "RUB", "USD", apiCache)
			if err == nil && rubAmount > 0 {
				res := m.formatInverseResult(rubAmount, "RUB", parsedRequest.Amount, "USD", scoreReverseConversion)
				if res != nil {
					results = append(results, *res)
				}
			}

			// 3. USD -> EUR
			res, _, err = m.generateConversionResult(parsedRequest, "EUR", apiCache, scoreQuickConversion)
			if err == nil && res != nil {
				results = append(results, *res)
			}

		case "EUR":
			// Fixed: Complete EUR handling matching the examples
			// 1. EUR -> RUB (direct conversion)
			res, _, err := m.generateConversionResult(parsedRequest, "RUB", apiCache, scoreBaseConversion)
			if err == nil && res != nil {
				results = append(results, *res)
			}

			// 2. EUR -> USD (direct conversion)
			res, _, err = m.generateConversionResult(parsedRequest, "USD", apiCache, scoreReverseConversion)
			if err == nil && res != nil {
				results = append(results, *res)
			}

			// 3. How much RUB needed to get X EUR (inverse)
			rubAmount, err := m.findInverseAmount(parsedRequest.Amount, "RUB", "EUR", apiCache)
			if err == nil && rubAmount > 0 {
				res := m.formatInverseResult(rubAmount, "RUB", parsedRequest.Amount, "EUR", scoreInverseConversion)
				if res != nil {
					results = append(results, *res)
				}
			}

		default:
			// Standard logic for other currencies
			handledTargets := make(map[string]bool)

			// Base conversion (to USD if configured)
			if m.baseConversionCurrency != "" && m.baseConversionCurrency != parsedRequest.FromCurrency {
				res, _, err := m.generateConversionResult(parsedRequest, m.baseConversionCurrency, apiCache, scoreBaseConversion)
				if err == nil && res != nil {
					results = append(results, *res)
					handledTargets[m.baseConversionCurrency] = true
				}
			}

			// Quick conversions
			for _, target := range m.quickConversionTargets {
				if target == parsedRequest.FromCurrency || handledTargets[target] {
					continue
				}
				res, _, err := m.generateConversionResult(parsedRequest, target, apiCache, scoreQuickConversion)
				if err == nil && res != nil {
					results = append(results, *res)
					handledTargets[target] = true
				}
			}

			// Always add RUB if not already handled and not the source
			if parsedRequest.FromCurrency != "RUB" && !handledTargets["RUB"] {
				res, _, err := m.generateConversionResult(parsedRequest, "RUB", apiCache, scoreQuickConversion-5)
				if err == nil && res != nil {
					results = append(results, *res)
				}
			}
		}
	}

	if len(results) == 0 {
		return []commontypes.FlowResult{}, nil // Return empty slice consistently
	}
	return results, nil
}
