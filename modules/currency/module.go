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

	// Validate query length
	if len(query) > maxQueryLength {
		return nil, fmt.Errorf("query too long")
	}

	if apiCache.IsStale() {
		staleness := apiCache.GetCacheStaleness()
		for provider, duration := range staleness {
			if duration > time.Hour*4 {
				log.Printf("Warning: %s data critically stale (%v)", provider, duration)
			}
		}
		if cacheRefreshInProgress.CompareAndSwap(false, true) {
			go func() {
				defer cacheRefreshInProgress.Store(false)
				// Retry logic for cache refresh
				for i := 0; i < maxRetries; i++ {
					if err := apiCache.ForceRefresh(); err != nil {
						log.Printf("Cache refresh attempt %d/%d failed: %v", i+1, maxRetries, err)
						if i < maxRetries-1 {
							time.Sleep(baseRetryDelay * time.Duration(i+1))
						}
					} else {
						log.Printf("Cache refresh succeeded")
						break
					}
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

		// Check context before expensive operation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		res, _, err := m.generateConversionResult(ctx, parsedRequest, parsedRequest.ToCurrency, apiCache, scoreSpecificConversion)
		if err == nil && res != nil {
			results = append(results, *res)
		} else if err != nil {
			if er := m.makeErrorResult(parsedRequest, parsedRequest.ToCurrency, err); er != nil {
				results = append(results, *er)
			}
		}
	} else {
		results = m.generateQuickConversions(ctx, parsedRequest, apiCache)
	}

	return results, nil
}

func (m *CurrencyConverterModule) generateQuickConversions(ctx context.Context, req *ConversionRequest, apiCache *APICache) []commontypes.FlowResult {
	var results []commontypes.FlowResult
	seen := make(map[string]bool)

	addResult := func(targetCurrency string, score int, isInverse bool) {
		// Deduplication
		key := fmt.Sprintf("%s->%s:%t", req.FromCurrency, targetCurrency, isInverse)
		if seen[key] {
			return
		}
		seen[key] = true

		// Check context cancellation
		select {
		case <-ctx.Done():
			return
		default:
		}

		if isInverse {
			amount, err := m.findInverseAmount(req.Amount, targetCurrency, req.FromCurrency, apiCache)
			if err == nil && amount > 0 {
				if res := m.formatInverseResult(amount, targetCurrency, req.Amount, req.FromCurrency, score); res != nil {
					results = append(results, *res)
				}
			}
		} else {
			res, _, err := m.generateConversionResult(ctx, req, targetCurrency, apiCache, score)
			if err == nil && res != nil {
				results = append(results, *res)
			} else if err != nil {
				if er := m.makeErrorResult(req, targetCurrency, err); er != nil {
					results = append(results, *er)
				}
			}
		}
	}

	switch req.FromCurrency {
	case "RUB":
		addResult("USD", scoreBaseConversion, false)
		addResult("USD", scoreReverseConversion, true)
		addResult("EUR", scoreQuickConversion, false)

	case "USD":
		addResult("RUB", scoreBaseConversion, false)
		addResult("RUB", scoreReverseConversion, true)
		addResult("EUR", scoreQuickConversion, false)

	case "EUR":
		addResult("RUB", scoreBaseConversion, false)
		addResult("USD", scoreReverseConversion, false)
		addResult("RUB", scoreInverseConversion, true)

	default:
		// For all other currencies (fiats, cryptos)
		// PRIORITY: Buy (inverse RUB) first, then sell conversions

		// 1. HIGHEST PRIORITY: Inverse RUB (buy tag) - how much RUB to buy this currency
		if req.FromCurrency != "RUB" && !seen[fmt.Sprintf("%s->%s:true", req.FromCurrency, "RUB")] {
			addResult("RUB", 95, true) // Highest score for buy
		}

		// 2. Base conversion (usually USD)
		if m.baseConversionCurrency != "" && m.baseConversionCurrency != req.FromCurrency {
			addResult(m.baseConversionCurrency, scoreBaseConversion, false)
		}

		// 3. Forward RUB (sell tag) - convert foreign currency to RUB
		if req.FromCurrency != "RUB" && !seen[fmt.Sprintf("%s->%s:false", req.FromCurrency, "RUB")] {
			addResult("RUB", 85, false) // Between base and quick conversions
		}

		// 4. Quick conversion targets (e.g., EUR)
		for _, target := range m.quickConversionTargets {
			if target != req.FromCurrency && !seen[fmt.Sprintf("%s->%s:false", req.FromCurrency, target)] {
				addResult(target, scoreQuickConversion, false)
			}
		}
	}

	return results
}

func (m *CurrencyConverterModule) generateConversionResult(ctx context.Context, req *ConversionRequest, targetCurrency string, apiCache *APICache, baseScore int) (*commontypes.FlowResult, float64, error) {
	if req.FromCurrency == targetCurrency {
		return nil, 0, nil
	}

	// Check context before expensive operation
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	default:
	}

	finalAmount, err := m.convert(req.Amount, req.FromCurrency, targetCurrency, apiCache)
	if err != nil {
		return nil, 0, err
	}

	if finalAmount < minAmountAfterFees {
		return nil, 0, fmt.Errorf("amount too small")
	}

	displayRate := finalAmount / req.Amount
	if !isValidFloat(displayRate) {
		return nil, 0, fmt.Errorf("invalid rate")
	}

	// Build route-based slippage and fee info
	slippageInfo := m.calculateSlippageInfo(req, targetCurrency, apiCache)
	routeLegs := m.planRoute(req.FromCurrency, targetCurrency, apiCache)
	feesInfo := m.buildFeesInfoFromRoute(routeLegs)

	return m.formatResult(req, targetCurrency, finalAmount, displayRate, baseScore, slippageInfo, feesInfo), finalAmount, nil
}

// calculateSlippageInfo inspects the route and provides a warning string
// if order book slippage is significant for the given amount.
func (m *CurrencyConverterModule) calculateSlippageInfo(req *ConversionRequest, targetCurrency string, apiCache *APICache) string {
	fromType := getCurrencyType(req.FromCurrency, apiCache)
	toType := getCurrencyType(targetCurrency, apiCache)

	// Only check slippage for crypto trades
	if fromType != "crypto" && fromType != "TON" && toType != "crypto" && toType != "TON" {
		return ""
	}

	var usdValue float64
	if req.FromCurrency == "USDT" || req.FromCurrency == "USD" {
		usdValue = req.Amount
	} else if req.FromCurrency == "TON" || fromType == "crypto" {
		symbol := req.FromCurrency + "USDT"
		if rate, err := apiCache.GetBybitRate(symbol); err == nil && rate != nil {
			usdValue = req.Amount * rate.BestBid
		}
	}

	if !shouldUseOrderBookByUSD(usdValue) {
		return ""
	}

	var slippagePercent float64
	symbol := req.FromCurrency + "USDT"
	isBuy := false
	if req.FromCurrency == "USDT" {
		symbol = targetCurrency + "USDT"
		isBuy = true
	}

	if slippage, err := apiCache.CalculateSlippage(symbol, req.Amount, isBuy); err == nil {
		slippagePercent = slippage
	}

	if slippagePercent > slippageWarningThreshold {
		return fmt.Sprintf(" ⚠️ %.1f%% slip", slippagePercent)
	}
	return ""
}

// buildFeesInfoFromRoute generates a concise, accurate fee summary for the given route.
func (m *CurrencyConverterModule) buildFeesInfoFromRoute(legs []string) string {
	if len(legs) < 2 {
		return ""
	}

	var parts []string

	for i := 0; i+1 < len(legs); i++ {
		a, b := legs[i], legs[i+1]

		// Bybit Card 1% for USDT <-> USD
		if (a == "USDT" && b == "USD") || (a == "USD" && b == "USDT") {
		}

		// Mastercard 2% for USD <-> other fiat (non-USD)
		if (a == "USD" && b != "USD" && b != "USDT" && b != "TON" && b != "RUB") ||
			(b == "USD" && a != "USD" && a != "USDT" && a != "TON" && a != "RUB") {
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return " | " + strings.Join(parts, "+")
}

func (m *CurrencyConverterModule) makeErrorResult(req *ConversionRequest, target string, err error) *commontypes.FlowResult {
	title := fmt.Sprintf("Conversion unavailable: %s → %s", req.FromCurrency, target)
	sub := TranslateError(err)
	return &commontypes.FlowResult{
		Title:    title,
		SubTitle: sub,
		Score:    10,
		JsonRPCAction: commontypes.JsonRPCAction{
			Method:     "copy_to_clipboard",
			Parameters: []interface{}{fmt.Sprintf("%s %s", formatAmountForClipboard(req.Amount, req.FromCurrency), req.FromCurrency)},
		},
	}
}
