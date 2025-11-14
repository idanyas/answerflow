package currency

import (
	"context"
	"fmt"
	"log"
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
	normalizedTargets := make([]string, len(quickTargets))
	for i, target := range quickTargets {
		normalizedTargets[i] = strings.ToUpper(target)
	}

	currencyData := NewCurrencyData()
	apiCurrencies := make(map[string]string)
	for _, crypto := range supportedCryptos {
		apiCurrencies[crypto] = crypto + " Cryptocurrency"
	}
	for _, fiat := range supportedFiats {
		apiCurrencies[fiat] = fiat + " Currency"
	}
	currencyData.PopulateDynamicAliases(apiCurrencies)

	return &CurrencyConverterModule{
		quickConversionTargets: normalizedTargets,
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

var cacheRefreshInProgress atomic.Bool

func (m *CurrencyConverterModule) ProcessQuery(ctx context.Context, query string, apiCache *APICache) ([]commontypes.FlowResult, error) {
	if apiCache == nil {
		return nil, fmt.Errorf("API cache not initialized")
	}

	if apiCache.IsStale() {
		staleness := apiCache.GetCacheStaleness()
		for provider, duration := range staleness {
			if duration > time.Hour*4 {
				log.Printf("WARNING: %s data critically stale (%v)", provider, duration)
			}
		}
		if cacheRefreshInProgress.CompareAndSwap(false, true) {
			go func() {
				defer cacheRefreshInProgress.Store(false)
				if err := apiCache.ForceRefresh(); err != nil {
					log.Printf("Failed to refresh cache: %v", err)
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

	if err := ValidateAmount(parsedRequest.Amount); err != nil {
		return nil, nil
	}

	var results []commontypes.FlowResult

	if parsedRequest.ToCurrency != "" {
		toCurrency, err := m.currencyData.ResolveCurrency(parsedRequest.ToCurrency)
		if err != nil {
			return nil, nil
		}
		parsedRequest.ToCurrency = toCurrency

		if parsedRequest.FromCurrency == parsedRequest.ToCurrency {
			result := commontypes.FlowResult{
				Title:    fmt.Sprintf("%s %s", formatAmount(parsedRequest.Amount, parsedRequest.FromCurrency), parsedRequest.FromCurrency),
				SubTitle: "Same currency",
				Score:    100,
				JsonRPCAction: commontypes.JsonRPCAction{
					Method:     "copy_to_clipboard",
					Parameters: []interface{}{formatAmountForClipboard(parsedRequest.Amount, parsedRequest.FromCurrency)},
				},
			}
			return []commontypes.FlowResult{result}, nil
		}

		res, _, err := m.generateConversionResult(parsedRequest, parsedRequest.ToCurrency, apiCache, scoreSpecificConversion)
		if err == nil && res != nil {
			results = append(results, *res)
		}
	} else {
		results = m.generateQuickConversions(parsedRequest, apiCache)
	}

	return results, nil
}

func (m *CurrencyConverterModule) generateQuickConversions(req *ConversionRequest, apiCache *APICache) []commontypes.FlowResult {
	var results []commontypes.FlowResult

	switch req.FromCurrency {
	case "RUB":
		if res, _, err := m.generateConversionResult(req, "USD", apiCache, scoreBaseConversion); err == nil && res != nil {
			results = append(results, *res)
		}
		if usdAmount, err := m.findInverseAmount(req.Amount, "USD", "RUB", apiCache); err == nil && usdAmount > 0 {
			if res := m.formatInverseResult(usdAmount, "USD", req.Amount, "RUB", scoreReverseConversion); res != nil {
				results = append(results, *res)
			}
		}
		if res, _, err := m.generateConversionResult(req, "EUR", apiCache, scoreQuickConversion); err == nil && res != nil {
			results = append(results, *res)
		}

	case "USD":
		if res, _, err := m.generateConversionResult(req, "RUB", apiCache, scoreBaseConversion); err == nil && res != nil {
			results = append(results, *res)
		}
		if rubAmount, err := m.findInverseAmount(req.Amount, "RUB", "USD", apiCache); err == nil && rubAmount > 0 {
			if res := m.formatInverseResult(rubAmount, "RUB", req.Amount, "USD", scoreReverseConversion); res != nil {
				results = append(results, *res)
			}
		}
		if res, _, err := m.generateConversionResult(req, "EUR", apiCache, scoreQuickConversion); err == nil && res != nil {
			results = append(results, *res)
		}

	case "EUR":
		if res, _, err := m.generateConversionResult(req, "RUB", apiCache, scoreBaseConversion); err == nil && res != nil {
			results = append(results, *res)
		}
		if res, _, err := m.generateConversionResult(req, "USD", apiCache, scoreReverseConversion); err == nil && res != nil {
			results = append(results, *res)
		}
		if rubAmount, err := m.findInverseAmount(req.Amount, "RUB", "EUR", apiCache); err == nil && rubAmount > 0 {
			if res := m.formatInverseResult(rubAmount, "RUB", req.Amount, "EUR", scoreInverseConversion); res != nil {
				results = append(results, *res)
			}
		}

	default:
		handledTargets := make(map[string]bool)
		if m.baseConversionCurrency != "" && m.baseConversionCurrency != req.FromCurrency {
			if res, _, err := m.generateConversionResult(req, m.baseConversionCurrency, apiCache, scoreBaseConversion); err == nil && res != nil {
				results = append(results, *res)
				handledTargets[m.baseConversionCurrency] = true
			}
		}

		for _, target := range m.quickConversionTargets {
			if target == req.FromCurrency || handledTargets[target] {
				continue
			}
			if res, _, err := m.generateConversionResult(req, target, apiCache, scoreQuickConversion); err == nil && res != nil {
				results = append(results, *res)
				handledTargets[target] = true
			}
		}

		if req.FromCurrency != "RUB" && !handledTargets["RUB"] {
			if res, _, err := m.generateConversionResult(req, "RUB", apiCache, scoreQuickConversion-5); err == nil && res != nil {
				results = append(results, *res)
			}
		}
	}

	return results
}

func (m *CurrencyConverterModule) generateConversionResult(req *ConversionRequest, targetCurrency string, apiCache *APICache, baseScore int) (*commontypes.FlowResult, float64, error) {
	if req.FromCurrency == targetCurrency {
		return nil, 0, nil
	}

	finalAmount, err := m.convert(req.Amount, req.FromCurrency, targetCurrency, apiCache)
	if err != nil {
		return nil, 0, nil
	}

	if finalAmount < minAmountAfterFees {
		return nil, 0, fmt.Errorf("amount too small")
	}

	displayRate := finalAmount / req.Amount
	if !isValidFloat(displayRate) {
		return nil, 0, fmt.Errorf("invalid rate")
	}

	slippageInfo := m.calculateSlippageInfo(req, targetCurrency, apiCache)

	return m.formatResult(req, targetCurrency, finalAmount, displayRate, baseScore, slippageInfo), finalAmount, nil
}

func (m *CurrencyConverterModule) calculateSlippageInfo(req *ConversionRequest, targetCurrency string, apiCache *APICache) string {
	if !shouldUseOrderBook(req.Amount, req.FromCurrency, targetCurrency, apiCache) {
		return ""
	}

	var slippagePercent float64
	if (req.FromCurrency == "TON" || getCurrencyType(req.FromCurrency, apiCache) == "crypto") &&
		(targetCurrency == "USDT" || getCurrencyType(targetCurrency, apiCache) == "crypto") {

		symbol := req.FromCurrency + "USDT"
		isBuy := false
		if req.FromCurrency == "USDT" {
			symbol = targetCurrency + "USDT"
			isBuy = true
		}

		if slippage, err := apiCache.CalculateSlippage(symbol, req.Amount, isBuy); err == nil {
			slippagePercent = slippage
		}
	}

	if slippagePercent > slippageWarningThreshold {
		return fmt.Sprintf(" ⚠️ %.2f%% slippage", slippagePercent)
	}
	return ""
}
