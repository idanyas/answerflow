package currency

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"answerflow/commontypes"

	"github.com/leekchan/accounting"
	"golang.org/x/time/rate"
)

const (
	scoreSpecificConversion = 100
	scoreBaseConversion     = 90
	scoreReverseConversion  = 85
	scoreQuickConversion    = 80
	scoreInverseConversion  = 75 // New score for inverse EUR calculations
)

// Fee constants - ALL FEES AS DECIMAL (0.01 = 1%)
const (
	// NOTE: Whitebird API provides exchange rates that ALREADY include their 1.5% fee
	// The 'ratio' field from the API is the final rate after fees
	// We do NOT apply additional 1.5% in our calculations
	feeBybitTrade             = 0.001  // 0.1% trading fee on Bybit
	feeUSDTToUSD              = 0.01   // 1% when converting USDT to USD (withdrawal from Bybit Card)
	feeUSDToUSDT              = 0.01   // 1% when converting USD to USDT (deposit to Bybit Card)
	feeMastercard             = 0.005  // 0.5% Mastercard conversion fee
	feeTONWithdrawToBybit     = 0.0025 // TON fixed withdrawal fee to Bybit
	feeTONWithdrawToWhitebird = 0.02   // TON fixed withdrawal fee to Whitebird

	// Error thresholds
	minAmountAfterFees  = 0.000001 // Minimum amount after fees to consider valid
	maxConversionAmount = 1e15     // Maximum amount to prevent overflow

	// Order book thresholds
	minLargeOrderUSDT = 100.0 // Minimum USDT value to consider large

	// Retry configuration for conversions
	conversionMaxRetries = 3
	conversionRetryDelay = 100 * time.Millisecond

	// Slippage warning threshold
	slippageWarningThreshold = 2.0 // 2% slippage triggers warning (as percentage)

	// Rate limit for API calls
	apiRateLimit = 100 // requests per minute (conservative to avoid hitting 120 limit)
)

var (
	highPrecisionCurrencies = map[string]int{
		"BTC": 8,
		"ETH": 6, // Fixed: ETH typically uses 6 decimals for display
		"TON": 6,
	}

	// Global rate limiter for API calls with proper burst
	apiRateLimiter = rate.NewLimiter(rate.Every(time.Minute/time.Duration(apiRateLimit)), apiRateLimit/3)
)

// conversionCache for deduplication with improved cleanup
type conversionCache struct {
	mu           sync.RWMutex
	results      map[string]*cachedResult
	lastCleanup  time.Time
	cleanupMutex sync.Mutex
}

type cachedResult struct {
	value     float64
	timestamp time.Time
}

var globalConversionCache = &conversionCache{
	results:     make(map[string]*cachedResult),
	lastCleanup: time.Now(),
}

func (c *conversionCache) get(key string) (float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if result, ok := c.results[key]; ok {
		if time.Since(result.timestamp) < 30*time.Second {
			return result.value, true
		}
	}
	return 0, false
}

func (c *conversionCache) set(key string, value float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.results[key] = &cachedResult{
		value:     value,
		timestamp: time.Now(),
	}

	// Improved cleanup: only run periodically, not on every set
	c.cleanupMutex.Lock()
	defer c.cleanupMutex.Unlock()
	if time.Since(c.lastCleanup) > 5*time.Minute {
		go func() {
			// Create a copy of keys to avoid holding lock during cleanup
			c.mu.RLock()
			keysToCheck := make([]string, 0, len(c.results))
			for k := range c.results {
				keysToCheck = append(keysToCheck, k)
			}
			c.mu.RUnlock()

			// Now clean up old entries
			now := time.Now()
			c.mu.Lock()
			for _, k := range keysToCheck {
				if v, ok := c.results[k]; ok {
					if now.Sub(v.timestamp) > time.Minute {
						delete(c.results, k)
					}
				}
			}
			c.mu.Unlock()
		}()
		c.lastCleanup = time.Now()
	}
}

// formatCacheKey creates consistent cache keys using string formatting to avoid float precision issues
func formatCacheKey(from, to string, amount float64) string {
	// Use scientific notation with fixed precision to avoid float comparison issues
	amountStr := strconv.FormatFloat(amount, 'e', 10, 64)
	return fmt.Sprintf("%s_%s_%s", from, to, amountStr)
}

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

	return &CurrencyConverterModule{
		quickConversionTargets: normalizedQuickTargets,
		baseConversionCurrency: strings.ToUpper(baseCurrency),
		defaultIconPath:        iconPath,
		currencyData:           NewCurrencyData(),
		ShortDisplayFormat:     shortDisplay,
	}
}

func (m *CurrencyConverterModule) Name() string {
	return "CurrencyConverter"
}

func (m *CurrencyConverterModule) DefaultIconPath() string {
	return m.defaultIconPath
}

func formatAmount(amount float64, currencyCode string) string {
	precision, isHighPrecision := highPrecisionCurrencies[currencyCode]
	if !isHighPrecision {
		precision = 2
	}
	ac := accounting.Accounting{Symbol: "", Precision: precision, Thousand: ",", Decimal: "."}
	return ac.FormatMoneyFloat64(amount)
}

func formatAmountForClipboard(amount float64, currencyCode string) string {
	// Fixed: Use currency-specific precision consistently
	precision, isHighPrecision := highPrecisionCurrencies[currencyCode]
	if !isHighPrecision {
		// For non-high-precision currencies, adjust based on amount
		if currencyCode == "BTC" {
			precision = 8 // BTC always needs 8
		} else if amount < 0.01 {
			precision = 6
		} else if amount < 1 {
			precision = 4
		} else {
			precision = 2
		}
	}
	// Format with precision but without thousand separators for clipboard
	return strconv.FormatFloat(amount, 'f', precision, 64)
}

// retryConversion wraps conversion with retry logic and rate limiting
func retryConversion(fn func() (float64, error)) (float64, error) {
	var lastErr error
	for i := 0; i < conversionMaxRetries; i++ {
		// Apply rate limiting
		if err := apiRateLimiter.Wait(context.Background()); err != nil {
			return 0, fmt.Errorf("rate limit error: %w", err)
		}

		result, err := fn()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if i < conversionMaxRetries-1 {
			time.Sleep(conversionRetryDelay * time.Duration(i+1))
		}
	}
	return 0, fmt.Errorf("conversion failed after %d retries: %w", conversionMaxRetries, lastErr)
}

// findInverseAmount calculates how much of sourceCurrency is needed to get targetAmount of targetCurrency
func (m *CurrencyConverterModule) findInverseAmount(targetAmount float64, sourceCurrency, targetCurrency string, apiCache *APICache) (float64, error) {
	// Validate inputs - including negative amounts
	if targetAmount <= 0 || targetAmount > maxConversionAmount || math.IsNaN(targetAmount) || math.IsInf(targetAmount, 0) {
		return 0, fmt.Errorf("invalid target amount: %f", targetAmount)
	}

	// Check cache first with proper key formatting
	cacheKey := formatCacheKey("inverse_"+sourceCurrency, targetCurrency, targetAmount)
	if cached, ok := globalConversionCache.get(cacheKey); ok {
		return cached, nil
	}

	// Use binary search to find the source amount that yields the target amount
	// Start with a rough estimate
	testRate, err := m.convert(1.0, sourceCurrency, targetCurrency, apiCache)
	if err != nil {
		return 0, fmt.Errorf("failed to get base rate: %w", err)
	}
	if testRate <= 0 {
		return 0, fmt.Errorf("invalid conversion rate")
	}

	// Initial estimate: if 1 source gives testRate target, then to get targetAmount we need approximately targetAmount/testRate
	estimate := targetAmount / testRate
	if estimate <= 0 || math.IsNaN(estimate) || math.IsInf(estimate, 0) {
		return 0, fmt.Errorf("invalid initial estimate for inverse conversion")
	}

	// Fixed: Wider search bounds for complex conversion chains with multiple fees
	low := estimate * 0.5  // Account for up to 50% total fees
	high := estimate * 2.0 // Allow for significant fee stacking

	// Binary search with tight tolerance
	tolerance := math.Max(targetAmount*0.00001, 0.000001) // 0.001% tolerance or minimum
	maxIterations := 100                                  // Increased iterations for wider bounds

	for i := 0; i < maxIterations; i++ {
		mid := (low + high) / 2.0
		if mid <= 0 || math.IsNaN(mid) || math.IsInf(mid, 0) {
			return 0, fmt.Errorf("invalid mid value in binary search")
		}

		result, err := m.convert(mid, sourceCurrency, targetCurrency, apiCache)
		if err != nil {
			return 0, fmt.Errorf("conversion failed in binary search: %w", err)
		}

		diff := result - targetAmount
		if math.Abs(diff) < tolerance {
			globalConversionCache.set(cacheKey, mid)
			return mid, nil
		}

		if result < targetAmount {
			low = mid
		} else {
			high = mid
		}

		// Prevent infinite loops
		if math.Abs(high-low) < 0.000001 {
			break
		}
	}

	finalAmount := (low + high) / 2.0
	if finalAmount <= 0 || math.IsNaN(finalAmount) || math.IsInf(finalAmount, 0) {
		return 0, fmt.Errorf("failed to find valid inverse amount")
	}

	globalConversionCache.set(cacheKey, finalAmount)
	return finalAmount, nil
}

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
		// Launch refresh in background without waiting
		go func() {
			if err := apiCache.ForceRefresh(); err != nil {
				log.Printf("Failed to refresh stale cache: %v", err)
			}
		}()
	}

	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	// Populate currency data with API currencies
	apiCurrencies := make(map[string]string)
	// Add known cryptos
	for _, crypto := range supportedCryptos {
		apiCurrencies[crypto] = crypto + " Cryptocurrency"
	}
	// Add known fiats
	for _, fiat := range supportedFiats {
		apiCurrencies[fiat] = fiat + " Currency"
	}
	m.currencyData.PopulateDynamicAliases(apiCurrencies)

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
				// Get the actual market rate for display
				marketRate, _ := m.getMarketRate("USD", "RUB", apiCache)
				res := m.formatInverseResult(usdAmount, "USD", parsedRequest.Amount, "RUB", marketRate, scoreReverseConversion)
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
				// Get the actual market rate for display
				marketRate, _ := m.getMarketRate("RUB", "USD", apiCache)
				res := m.formatInverseResult(rubAmount, "RUB", parsedRequest.Amount, "USD", marketRate, scoreReverseConversion)
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

			// 3. How much RUB needed to get X EUR (inverse) - matching the example "3,528.96 RUB"
			rubAmount, err := m.findInverseAmount(parsedRequest.Amount, "RUB", "EUR", apiCache)
			if err == nil && rubAmount > 0 {
				// Get the actual market rate for display
				marketRate, _ := m.getMarketRate("RUB", "EUR", apiCache)
				res := m.formatInverseResult(rubAmount, "RUB", parsedRequest.Amount, "EUR", marketRate, scoreInverseConversion)
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

// getMarketRate returns the actual market exchange rate between two currencies
func (m *CurrencyConverterModule) getMarketRate(from, to string, apiCache *APICache) (float64, error) {
	// Convert 1 unit to get the base rate
	rate, err := m.convert(1.0, from, to, apiCache)
	if err != nil {
		return 0, err
	}
	return rate, nil
}

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
		return nil, 0, err
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
	if m.shouldUseOrderBook(req.Amount, fromCurrency, targetCurrency, apiCache) {
		slippagePercent := m.calculateSlippage(req.Amount, fromCurrency, targetCurrency, apiCache)
		if slippagePercent > slippageWarningThreshold {
			// Show slippage as percentage
			slippageInfo = fmt.Sprintf(" ⚠️ %.2f%% slippage", slippagePercent)
		}
	}

	return m.formatResult(req, targetCurrency, finalAmount, displayRate, baseScore, slippageInfo), finalAmount, nil
}

// formatInverseResult creates a display result for inverse conversions with correct market rates
func (m *CurrencyConverterModule) formatInverseResult(sourceAmount float64, sourceCurrency string, targetAmount float64, targetCurrency string, marketRate float64, score int) *commontypes.FlowResult {
	var title, subTitle, clipboardText string

	// Fixed: Use actual market rate for display, not the calculation ratio
	var tag string
	var rateStr string

	hasRub := sourceCurrency == "RUB" || targetCurrency == "RUB"
	hasUsd := sourceCurrency == "USD" || sourceCurrency == "USDT" || targetCurrency == "USD" || targetCurrency == "USDT"

	if hasRub && hasUsd {
		if sourceCurrency == "USD" && targetCurrency == "RUB" {
			// Need X USD to buy Y RUB = buying RUB with USD = купить
			tag = " 🛍️ купить"
			// Show rate as 1 USD = X RUB (standard format)
			if marketRate > 0 {
				rateStr = fmt.Sprintf("1 %s = %s %s", sourceCurrency, formatRate(marketRate), targetCurrency)
			}
		} else if sourceCurrency == "RUB" && targetCurrency == "USD" {
			// Need X RUB to get Y USD = selling RUB for USD = продать
			tag = " 🏷️ продать"
			// Show rate as 1 USD = X RUB (standard format)
			if marketRate > 0 {
				// For RUB->USD, marketRate is USD per RUB, we want to show RUB per USD
				rubPerUsd := 1.0 / marketRate
				rateStr = fmt.Sprintf("1 %s = %s %s", targetCurrency, formatRate(rubPerUsd), sourceCurrency)
			}
		}
	}

	// Default rate string if not set
	if rateStr == "" {
		// Show the market rate for the conversion
		if marketRate > 0 {
			rateStr = fmt.Sprintf("1 %s = %s %s", sourceCurrency, formatRate(marketRate), targetCurrency)
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

	return &commontypes.FlowResult{
		Title:    title,
		SubTitle: subTitle,
		Score:    score,
		JsonRPCAction: commontypes.JsonRPCAction{
			Method:     "copy_to_clipboard",
			Parameters: []interface{}{clipboardText},
		},
		// Could add ContextMenuItems here if needed for additional actions
	}
}

// convert performs the actual conversion logic with order book depth support
func (m *CurrencyConverterModule) convert(amount float64, from, to string, apiCache *APICache) (float64, error) {
	// Early same-currency check
	if from == to {
		return amount, nil
	}

	// Validate input - including negative amount check
	if amount <= 0 || amount > maxConversionAmount || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0, fmt.Errorf("amount must be positive and less than %g", maxConversionAmount)
	}

	// Check cache first with proper key formatting
	cacheKey := formatCacheKey(from, to, amount)
	if cached, ok := globalConversionCache.get(cacheKey); ok {
		return cached, nil
	}

	// Determine currency types
	fromType := m.getCurrencyType(from, apiCache)
	toType := m.getCurrencyType(to, apiCache)

	var result float64
	var err error

	switch {
	// RUB <-> TON (direct via Whitebird)
	case fromType == "RUB" && toType == "TON":
		result, err = m.convertRUBToTONDirect(amount, apiCache)

	case fromType == "TON" && toType == "RUB":
		result, err = m.convertTONToRUBDirect(amount, apiCache)

	// RUB -> Crypto (not TON)
	case fromType == "RUB" && toType == "crypto":
		result, err = m.convertRUBToCrypto(amount, to, apiCache)

	// RUB -> Fiat
	case fromType == "RUB" && toType == "fiat":
		result, err = m.convertRUBToFiat(amount, to, apiCache)

	// Crypto -> RUB
	case fromType == "crypto" && toType == "RUB":
		result, err = m.convertCryptoToRUB(amount, from, apiCache)

	// Fiat -> RUB
	case fromType == "fiat" && toType == "RUB":
		result, err = m.convertFiatToRUB(amount, from, apiCache)

	// Crypto <-> Crypto
	case fromType == "crypto" && toType == "crypto":
		result, err = m.convertCryptoToCrypto(amount, from, to, apiCache)

	// Fiat <-> Fiat
	case fromType == "fiat" && toType == "fiat":
		result, err = m.convertFiatToFiat(amount, from, to, apiCache)

	// TON -> Crypto
	case fromType == "TON" && toType == "crypto":
		result, err = m.convertTONToCrypto(amount, to, apiCache)

	// Crypto -> TON
	case fromType == "crypto" && toType == "TON":
		result, err = m.convertCryptoToTON(amount, from, apiCache)

	// TON -> Fiat
	case fromType == "TON" && toType == "fiat":
		result, err = m.convertTONToFiat(amount, to, apiCache)

	// Fiat -> TON
	case fromType == "fiat" && toType == "TON":
		result, err = m.convertFiatToTONDirect(amount, from, apiCache)

	default:
		err = fmt.Errorf("unsupported conversion: %s (%s) -> %s (%s)", from, fromType, to, toType)
	}

	if err == nil && result > 0 {
		globalConversionCache.set(cacheKey, result)
	}

	return result, err
}

// Helper: get currency type with special handling for USDT
func (m *CurrencyConverterModule) getCurrencyType(code string, apiCache *APICache) string {
	if code == "RUB" {
		return "RUB"
	}
	if code == "TON" {
		return "TON"
	}
	// USDT is treated as crypto for trading purposes
	if apiCache.IsCrypto(code) {
		return "crypto"
	}
	if apiCache.IsFiat(code) {
		return "fiat"
	}
	return "unknown"
}

// shouldUseOrderBook determines if order book depth is needed
func (m *CurrencyConverterModule) shouldUseOrderBook(amount float64, from, to string, apiCache *APICache) bool {
	// For crypto conversions, check if amount is significant
	fromType := m.getCurrencyType(from, apiCache)
	toType := m.getCurrencyType(to, apiCache)

	if fromType != "crypto" && toType != "crypto" && fromType != "TON" && toType != "TON" {
		return false // No order book for pure fiat
	}

	// Estimate USD value
	var usdValue float64
	if from == "USDT" {
		usdValue = amount
	} else if from == "USD" {
		usdValue = amount
	} else if from == "TON" {
		// Get TON price
		if rate, err := apiCache.GetBybitRate("TONUSDT"); err == nil && rate != nil {
			usdValue = amount * rate.BestBid
		}
	} else if fromType == "crypto" {
		// Get crypto price
		if rate, err := apiCache.GetBybitRate(from + "USDT"); err == nil && rate != nil {
			usdValue = amount * rate.BestBid
		}
	}

	return usdValue > minLargeOrderUSDT
}

// calculateSlippage returns percentage directly
func (m *CurrencyConverterModule) calculateSlippage(amount float64, from, to string, apiCache *APICache) float64 {
	// Simplified slippage calculation
	if from == "TON" || to == "TON" {
		if slippage, err := apiCache.CalculateSlippage("TONUSDT", amount, from == "TON"); err == nil {
			return slippage // Already in percentage form from API
		}
	}
	return 0
}

// Conversion implementations with order book support

// RUB -> TON (direct with withdrawal fee per spec Case 1)
// The Whitebird API 'ratio' field already includes the 1.5% fee
func (m *CurrencyConverterModule) convertRUBToTONDirect(amount float64, apiCache *APICache) (float64, error) {
	rate, err := apiCache.GetWhitebirdRate("RUB", "TON")
	if err != nil {
		return 0, fmt.Errorf("failed to get Whitebird RUB->TON rate: %w", err)
	}
	if rate <= 0 {
		return 0, fmt.Errorf("invalid Whitebird rate: %f", rate)
	}
	// Step 1: Convert using Whitebird rate (fee already included in 'ratio' from API)
	// Rate is RUB per TON, so to get TON from RUB we divide
	ton := amount / rate
	// Step 2: Deduct withdrawal fee to Bybit
	ton -= feeTONWithdrawToBybit
	if ton <= 0 {
		return 0, fmt.Errorf("amount too small after withdrawal fee")
	}
	return ton, nil
}

// TON -> RUB (direct with withdrawal fee per spec Case 2)
// The Whitebird API 'ratio' field already includes the 1.5% fee
func (m *CurrencyConverterModule) convertTONToRUBDirect(amount float64, apiCache *APICache) (float64, error) {
	// Step 1: Deduct withdrawal fee to Whitebird (assumes TON starts on Bybit)
	ton := amount - feeTONWithdrawToWhitebird
	if ton <= 0 {
		return 0, fmt.Errorf("amount too small after withdrawal fee")
	}

	rate, err := apiCache.GetWhitebirdRate("TON", "RUB")
	if err != nil {
		return 0, fmt.Errorf("failed to get Whitebird TON->RUB rate: %w", err)
	}
	if rate <= 0 {
		return 0, fmt.Errorf("invalid Whitebird rate: %f", rate)
	}
	// Step 2: Convert using Whitebird rate (fee already included in 'ratio' from API)
	// Rate is RUB per TON, so to get RUB from TON we multiply
	result := ton * rate
	if result <= 0 {
		return 0, fmt.Errorf("amount too small after conversion")
	}
	return result, nil
}

// RUB -> Crypto (RUB -> TON -> withdraw -> USDT -> Crypto)
func (m *CurrencyConverterModule) convertRUBToCrypto(amount float64, toCrypto string, apiCache *APICache) (float64, error) {
	// Step 1: RUB -> TON (Whitebird rate includes fee)
	rate, err := apiCache.GetWhitebirdRate("RUB", "TON")
	if err != nil {
		return 0, fmt.Errorf("step 1 - failed to get Whitebird RUB->TON rate: %w", err)
	}
	if rate <= 0 {
		return 0, fmt.Errorf("step 1 - invalid Whitebird rate: %f", rate)
	}
	// Rate is RUB per TON, so divide to get TON
	ton := amount / rate

	// Step 2: Withdraw to Bybit
	ton -= feeTONWithdrawToBybit
	if ton <= 0 {
		return 0, fmt.Errorf("step 2 - amount too small after withdrawal fee")
	}

	// Step 3: TON -> USDT with order book depth
	usdt, err := m.convertTONToUSDT(ton, apiCache)
	if err != nil {
		return 0, fmt.Errorf("step 3 - %w", err)
	}

	if toCrypto == "USDT" {
		return usdt, nil
	}

	// Step 4: USDT -> target crypto with order book depth
	result, err := m.convertUSDTToCrypto(usdt, toCrypto, apiCache)
	if err != nil {
		return 0, fmt.Errorf("step 4 - %w", err)
	}
	return result, nil
}

// RUB -> Fiat (RUB -> TON -> withdraw -> USDT -> USD -> Fiat)
func (m *CurrencyConverterModule) convertRUBToFiat(amount float64, toFiat string, apiCache *APICache) (float64, error) {
	// Special case: USD with simplified chain
	if toFiat == "USD" {
		// RUB -> TON -> USDT -> USD (no Mastercard needed)
		rate, err := apiCache.GetWhitebirdRate("RUB", "TON")
		if err != nil {
			return 0, fmt.Errorf("failed to get Whitebird RUB->TON rate: %w", err)
		}
		if rate <= 0 {
			return 0, fmt.Errorf("invalid Whitebird rate: %f", rate)
		}
		ton := amount / rate
		ton -= feeTONWithdrawToBybit
		if ton <= 0 {
			return 0, fmt.Errorf("amount too small after withdrawal fee")
		}

		usdt, err := m.convertTONToUSDT(ton, apiCache)
		if err != nil {
			return 0, fmt.Errorf("failed to convert TON to USDT: %w", err)
		}

		// USDT to USD with Bybit Card fee
		usd := usdt * (1 - feeUSDTToUSD)
		if usd <= 0 {
			return 0, fmt.Errorf("amount too small after USDT->USD fee")
		}
		return usd, nil
	}

	// Step 1: RUB -> TON (Whitebird rate includes fee)
	rate, err := apiCache.GetWhitebirdRate("RUB", "TON")
	if err != nil {
		return 0, fmt.Errorf("step 1 - failed to get Whitebird RUB->TON rate: %w", err)
	}
	if rate <= 0 {
		return 0, fmt.Errorf("step 1 - invalid Whitebird rate: %f", rate)
	}
	ton := amount / rate

	// Step 2: Withdraw to Bybit
	ton -= feeTONWithdrawToBybit
	if ton <= 0 {
		return 0, fmt.Errorf("step 2 - amount too small after withdrawal fee")
	}

	// Step 3: TON -> USDT with order book depth
	usdt, err := m.convertTONToUSDT(ton, apiCache)
	if err != nil {
		return 0, fmt.Errorf("step 3 - %w", err)
	}

	// Step 4: USDT -> USD (Bybit Card withdrawal fee)
	usd := usdt * (1 - feeUSDTToUSD)
	if usd <= 0 {
		return 0, fmt.Errorf("step 4 - amount too small after USDT->USD fee")
	}

	// Step 5: USD -> target fiat (Mastercard with fee)
	mcRate, err := apiCache.GetMastercardRate("USD", toFiat)
	if err != nil {
		return 0, fmt.Errorf("step 5 - failed to get Mastercard USD->%s rate: %w", toFiat, err)
	}
	if mcRate <= 0 {
		return 0, fmt.Errorf("step 5 - invalid Mastercard rate: %f", mcRate)
	}

	result := usd * mcRate * (1 - feeMastercard)
	if result <= 0 {
		return 0, fmt.Errorf("step 5 - amount too small after conversion")
	}
	return result, nil
}

// Crypto -> RUB (Crypto -> USDT -> TON -> withdraw -> RUB)
func (m *CurrencyConverterModule) convertCryptoToRUB(amount float64, fromCrypto string, apiCache *APICache) (float64, error) {
	// Step 1: Crypto -> USDT with order book depth
	var usdt float64
	var err error

	if fromCrypto == "USDT" {
		usdt = amount
	} else {
		usdt, err = m.convertCryptoToUSDT(amount, fromCrypto, apiCache)
		if err != nil {
			return 0, fmt.Errorf("step 1 - %w", err)
		}
	}

	// Step 2: USDT -> TON with order book depth
	ton, err := m.convertUSDTToTON(usdt, apiCache)
	if err != nil {
		return 0, fmt.Errorf("step 2 - %w", err)
	}

	// Step 3: Withdraw to Whitebird
	ton -= feeTONWithdrawToWhitebird
	if ton <= 0 {
		return 0, fmt.Errorf("step 3 - amount too small after withdrawal fee")
	}

	// Step 4: TON -> RUB (Whitebird rate includes fee)
	rate, err := apiCache.GetWhitebirdRate("TON", "RUB")
	if err != nil {
		return 0, fmt.Errorf("step 4 - failed to get Whitebird TON->RUB rate: %w", err)
	}
	if rate <= 0 {
		return 0, fmt.Errorf("step 4 - invalid Whitebird rate: %f", rate)
	}
	// Rate is RUB per TON, so multiply to get RUB
	result := ton * rate
	if result <= 0 {
		return 0, fmt.Errorf("step 4 - amount too small after conversion")
	}
	return result, nil
}

// Fiat -> RUB (Fiat -> USD -> USDT -> TON -> withdraw -> RUB)
func (m *CurrencyConverterModule) convertFiatToRUB(amount float64, fromFiat string, apiCache *APICache) (float64, error) {
	// Simplified USD case - no unnecessary USD->USDT->USD conversion
	var usdt float64
	if fromFiat == "USD" {
		// Direct USD -> USDT with fee
		usdt = amount * (1 - feeUSDToUSDT)
		if usdt <= 0 {
			return 0, fmt.Errorf("amount too small after USD->USDT fee")
		}
	} else {
		// Step 1: Fiat -> USD via Mastercard
		mcRate, err := apiCache.GetMastercardRate(fromFiat, "USD")
		if err != nil {
			return 0, fmt.Errorf("step 1 - failed to get Mastercard %s->USD rate: %w", fromFiat, err)
		}
		if mcRate <= 0 {
			return 0, fmt.Errorf("step 1 - invalid Mastercard rate: %f", mcRate)
		}
		usd := amount * mcRate * (1 - feeMastercard)
		if usd <= 0 {
			return 0, fmt.Errorf("step 1 - amount too small after Mastercard fee")
		}

		// Step 2: USD -> USDT
		usdt = usd * (1 - feeUSDToUSDT)
		if usdt <= 0 {
			return 0, fmt.Errorf("step 2 - amount too small after USD->USDT fee")
		}
	}

	// Step 3: USDT -> TON with order book depth
	ton, err := m.convertUSDTToTON(usdt, apiCache)
	if err != nil {
		return 0, fmt.Errorf("step 3 - %w", err)
	}

	// Step 4: Withdraw to Whitebird
	ton -= feeTONWithdrawToWhitebird
	if ton <= 0 {
		return 0, fmt.Errorf("step 4 - amount too small after withdrawal fee")
	}

	// Step 5: TON -> RUB (Whitebird rate includes fee)
	rate, err := apiCache.GetWhitebirdRate("TON", "RUB")
	if err != nil {
		return 0, fmt.Errorf("step 5 - failed to get Whitebird TON->RUB rate: %w", err)
	}
	if rate <= 0 {
		return 0, fmt.Errorf("step 5 - invalid Whitebird rate: %f", rate)
	}
	// Rate is RUB per TON, so multiply to get RUB
	result := ton * rate
	if result <= 0 {
		return 0, fmt.Errorf("step 5 - amount too small after conversion")
	}
	return result, nil
}

// Crypto <-> Crypto (via USDT) with order book depth
func (m *CurrencyConverterModule) convertCryptoToCrypto(amount float64, from, to string, apiCache *APICache) (float64, error) {
	if from == "USDT" {
		return m.convertUSDTToCrypto(amount, to, apiCache)
	}
	if to == "USDT" {
		return m.convertCryptoToUSDT(amount, from, apiCache)
	}

	// Convert via USDT
	usdt, err := m.convertCryptoToUSDT(amount, from, apiCache)
	if err != nil {
		return 0, err
	}

	return m.convertUSDTToCrypto(usdt, to, apiCache)
}

// Fiat <-> Fiat (via USD with Mastercard)
func (m *CurrencyConverterModule) convertFiatToFiat(amount float64, from, to string, apiCache *APICache) (float64, error) {
	// Step 1: Convert to USD
	var usd float64
	if from == "USD" {
		usd = amount
	} else {
		mcRate, err := apiCache.GetMastercardRate(from, "USD")
		if err != nil {
			return 0, fmt.Errorf("failed to get Mastercard %s->USD rate: %w", from, err)
		}
		if mcRate <= 0 {
			return 0, fmt.Errorf("invalid Mastercard rate: %f", mcRate)
		}
		// Convert from source fiat to USD with Mastercard fee
		usd = amount * mcRate * (1 - feeMastercard)
		if usd <= 0 {
			return 0, fmt.Errorf("amount too small after Mastercard fee")
		}
	}

	if to == "USD" {
		return usd, nil
	}

	// Step 2: USD to target fiat with fee
	mcRate, err := apiCache.GetMastercardRate("USD", to)
	if err != nil {
		return 0, fmt.Errorf("failed to get Mastercard USD->%s rate: %w", to, err)
	}
	if mcRate <= 0 {
		return 0, fmt.Errorf("invalid Mastercard rate: %f", mcRate)
	}

	result := usd * mcRate * (1 - feeMastercard)
	if result <= 0 {
		return 0, fmt.Errorf("amount too small after conversion")
	}
	return result, nil
}

// TON -> Crypto (TON -> USDT -> Crypto, no withdrawal as TON is already on Bybit)
func (m *CurrencyConverterModule) convertTONToCrypto(amount float64, toCrypto string, apiCache *APICache) (float64, error) {
	usdt, err := m.convertTONToUSDT(amount, apiCache)
	if err != nil {
		return 0, err
	}
	if toCrypto == "USDT" {
		return usdt, nil
	}
	return m.convertUSDTToCrypto(usdt, toCrypto, apiCache)
}

// Crypto -> TON (Crypto -> USDT -> TON, no withdrawal)
func (m *CurrencyConverterModule) convertCryptoToTON(amount float64, fromCrypto string, apiCache *APICache) (float64, error) {
	var usdt float64
	var err error
	if fromCrypto == "USDT" {
		usdt = amount
	} else {
		usdt, err = m.convertCryptoToUSDT(amount, fromCrypto, apiCache)
		if err != nil {
			return 0, err
		}
	}
	return m.convertUSDTToTON(usdt, apiCache)
}

// TON -> Fiat (TON -> USDT -> USD -> Fiat, no withdrawal)
func (m *CurrencyConverterModule) convertTONToFiat(amount float64, toFiat string, apiCache *APICache) (float64, error) {
	usdt, err := m.convertTONToUSDT(amount, apiCache)
	if err != nil {
		return 0, err
	}

	usd := usdt * (1 - feeUSDTToUSD)
	if usd <= 0 {
		return 0, fmt.Errorf("amount too small after USDT->USD fee")
	}

	if toFiat == "USD" {
		return usd, nil
	}

	mcRate, err := apiCache.GetMastercardRate("USD", toFiat)
	if err != nil {
		return 0, fmt.Errorf("failed to get Mastercard USD->%s rate: %w", toFiat, err)
	}
	if mcRate <= 0 {
		return 0, fmt.Errorf("invalid Mastercard rate: %f", mcRate)
	}

	result := usd * mcRate * (1 - feeMastercard)
	if result <= 0 {
		return 0, fmt.Errorf("amount too small after conversion")
	}
	return result, nil
}

// Fiat -> TON (Fiat -> USD -> USDT -> TON, result on Bybit)
func (m *CurrencyConverterModule) convertFiatToTONDirect(amount float64, fromFiat string, apiCache *APICache) (float64, error) {
	var usd float64
	if fromFiat == "USD" {
		usd = amount
	} else {
		mcRate, err := apiCache.GetMastercardRate(fromFiat, "USD")
		if err != nil {
			return 0, fmt.Errorf("failed to get Mastercard %s->USD rate: %w", fromFiat, err)
		}
		if mcRate <= 0 {
			return 0, fmt.Errorf("invalid Mastercard rate: %f", mcRate)
		}
		// Convert from fiat to USD with Mastercard fee
		usd = amount * mcRate * (1 - feeMastercard)
		if usd <= 0 {
			return 0, fmt.Errorf("amount too small after Mastercard fee")
		}
	}

	// USD -> USDT (apply fee)
	usdt := usd * (1 - feeUSDToUSDT)
	if usdt <= 0 {
		return 0, fmt.Errorf("amount too small after USD->USDT fee")
	}
	return m.convertUSDTToTON(usdt, apiCache)
}

// Bybit conversions with order book support

// TON -> USDT (selling TON for USDT) with order book depth
func (m *CurrencyConverterModule) convertTONToUSDT(amount float64, apiCache *APICache) (float64, error) {
	// Get rate first for validation
	rate, err := apiCache.GetBybitRate("TONUSDT")
	if err != nil {
		return 0, fmt.Errorf("failed to get Bybit TONUSDT rate: %w", err)
	}
	if rate == nil || rate.BestBid <= 0 {
		return 0, fmt.Errorf("invalid Bybit bid rate")
	}

	// Check if we should use order book depth
	if m.shouldUseOrderBook(amount, "TON", "USDT", apiCache) {
		avgPrice, err := apiCache.GetBybitRateForAmount("TONUSDT", amount, false) // false = selling
		if err != nil {
			log.Printf("Failed to get order book price for %.2f TON, falling back to best bid: %v", amount, err)
			// Fallback to best bid
			result := amount * rate.BestBid * (1 - feeBybitTrade)
			if result <= 0 {
				return 0, fmt.Errorf("amount too small after trading")
			}
			return result, nil
		}
		// Use average execution price
		result := amount * avgPrice * (1 - feeBybitTrade)
		if result <= 0 {
			return 0, fmt.Errorf("amount too small after trading")
		}
		return result, nil
	}

	// Small order: use best bid
	result := amount * rate.BestBid * (1 - feeBybitTrade)
	if result <= 0 {
		return 0, fmt.Errorf("amount too small after trading")
	}
	return result, nil
}

// Fixed: USDT -> TON with proper order book calculation for buying
func (m *CurrencyConverterModule) convertUSDTToTON(usdt float64, apiCache *APICache) (float64, error) {
	// Apply fee to USDT before buying
	usdtAfterFee := usdt * (1 - feeBybitTrade)
	if usdtAfterFee <= 0 {
		return 0, fmt.Errorf("amount too small after trading fee")
	}

	// Check if we should use order book depth
	if m.shouldUseOrderBook(usdt, "USDT", "TON", apiCache) {
		// Calculate how much TON we can buy with this USDT amount (after fee)
		ton, avgPrice, err := apiCache.CalculateBuyAmountWithUSDT("TONUSDT", usdtAfterFee)
		if err != nil || ton <= 0 {
			// Fallback to simple calculation
			rate, err := apiCache.GetBybitRate("TONUSDT")
			if err != nil {
				return 0, fmt.Errorf("failed to get Bybit TONUSDT rate: %w", err)
			}
			if rate == nil || rate.BestAsk <= 0 {
				return 0, fmt.Errorf("invalid Bybit ask rate")
			}
			result := usdtAfterFee / rate.BestAsk
			if result <= 0 {
				return 0, fmt.Errorf("amount too small after trading")
			}
			return result, nil
		}

		if ton <= 0 {
			return 0, fmt.Errorf("amount too small after trading")
		}
		log.Printf("Bought %.6f TON with %.2f USDT (after fee) at avg price %.4f", ton, usdtAfterFee, avgPrice)
		return ton, nil
	}

	// Small order: use best ask
	rate, err := apiCache.GetBybitRate("TONUSDT")
	if err != nil {
		return 0, fmt.Errorf("failed to get Bybit TONUSDT rate: %w", err)
	}
	if rate == nil || rate.BestAsk <= 0 {
		return 0, fmt.Errorf("invalid Bybit ask rate")
	}

	result := usdtAfterFee / rate.BestAsk
	if result <= 0 {
		return 0, fmt.Errorf("amount too small after trading")
	}
	return result, nil
}

// Fixed: USDT -> Crypto with proper order book calculation for buying
func (m *CurrencyConverterModule) convertUSDTToCrypto(usdt float64, toCrypto string, apiCache *APICache) (float64, error) {
	symbol := toCrypto + "USDT"

	// Apply fee to USDT before buying
	usdtAfterFee := usdt * (1 - feeBybitTrade)
	if usdtAfterFee <= 0 {
		return 0, fmt.Errorf("amount too small after trading fee")
	}

	// Check if we should use order book depth
	if m.shouldUseOrderBook(usdt, "USDT", toCrypto, apiCache) {
		// Calculate how much crypto we can buy with this USDT amount (after fee)
		crypto, avgPrice, err := apiCache.CalculateBuyAmountWithUSDT(symbol, usdtAfterFee)
		if err != nil || crypto <= 0 {
			// Fallback to simple calculation
			rate, err := apiCache.GetBybitRate(symbol)
			if err != nil {
				return 0, fmt.Errorf("unsupported Bybit pair %s: %w", symbol, err)
			}
			if rate == nil || rate.BestAsk <= 0 {
				return 0, fmt.Errorf("invalid Bybit ask rate for %s", symbol)
			}
			result := usdtAfterFee / rate.BestAsk
			if result <= 0 {
				return 0, fmt.Errorf("amount too small after trading")
			}
			return result, nil
		}

		if crypto <= 0 {
			return 0, fmt.Errorf("amount too small after trading")
		}
		log.Printf("Bought %.8f %s with %.2f USDT (after fee) at avg price %.6f", crypto, toCrypto, usdtAfterFee, avgPrice)
		return crypto, nil
	}

	// Small order: use best ask
	rate, err := apiCache.GetBybitRate(symbol)
	if err != nil {
		return 0, fmt.Errorf("unsupported Bybit pair %s: %w", symbol, err)
	}
	if rate == nil || rate.BestAsk <= 0 {
		return 0, fmt.Errorf("invalid Bybit ask rate for %s", symbol)
	}

	result := usdtAfterFee / rate.BestAsk
	if result <= 0 {
		return 0, fmt.Errorf("amount too small after trading")
	}
	return result, nil
}

// Crypto -> USDT (selling crypto for USDT) with order book depth
func (m *CurrencyConverterModule) convertCryptoToUSDT(amount float64, fromCrypto string, apiCache *APICache) (float64, error) {
	symbol := fromCrypto + "USDT"

	// Validate symbol exists
	rate, err := apiCache.GetBybitRate(symbol)
	if err != nil {
		return 0, fmt.Errorf("unsupported Bybit pair %s: %w", symbol, err)
	}
	if rate == nil || rate.BestBid <= 0 {
		return 0, fmt.Errorf("invalid Bybit bid rate for %s", symbol)
	}

	// Check if we should use order book depth
	estimatedUSDT := amount * rate.BestBid
	if estimatedUSDT > minLargeOrderUSDT {
		avgPrice, err := apiCache.GetBybitRateForAmount(symbol, amount, false) // false = selling
		if err != nil {
			log.Printf("Failed to get order book price for selling %.8f %s, falling back to best bid: %v", amount, fromCrypto, err)
			// Fallback to best bid
			result := amount * rate.BestBid * (1 - feeBybitTrade)
			if result <= 0 {
				return 0, fmt.Errorf("amount too small after trading")
			}
			return result, nil
		}
		// Use average execution price
		result := amount * avgPrice * (1 - feeBybitTrade)
		if result <= 0 {
			return 0, fmt.Errorf("amount too small after trading")
		}
		return result, nil
	}

	// Small order: use best bid
	result := amount * rate.BestBid * (1 - feeBybitTrade)
	if result <= 0 {
		return 0, fmt.Errorf("amount too small after trading")
	}
	return result, nil
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

	// Always show rate in standard format: 1 USD = X RUB format for USD/RUB pairs
	var rateStr string
	if (hasRubFrom || hasRubTo) && (hasUsdFrom || hasUsdTo) {
		// For RUB/USD pairs, always show as 1 USD = X RUB
		if hasRubFrom && hasUsdTo {
			// RUB -> USD: show how many RUB per 1 USD
			if displayRate > 0 {
				rubPerUsd := 1.0 / displayRate
				rateStr = fmt.Sprintf("1 %s = %s %s", targetCurrency, formatRate(rubPerUsd), req.FromCurrency)
			}
		} else if hasUsdFrom && hasRubTo {
			// USD -> RUB: already in correct format
			rateStr = fmt.Sprintf("1 %s = %s %s", req.FromCurrency, formatRate(displayRate), targetCurrency)
		}
	} else {
		// Standard rate display for other pairs
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
		// Could add ContextMenuItems here for additional actions like:
		// - Copy rate to clipboard
		// - Open exchange in browser
		// - Show conversion details
	}
}

// Fixed: formatRate with better edge case handling
func formatRate(rate float64) string {
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return "N/A"
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

	// Safely trim trailing zeros and decimal point
	if strings.Contains(formattedRate, ".") {
		formattedRate = strings.TrimRight(formattedRate, "0")
		formattedRate = strings.TrimRight(formattedRate, ".")
	}

	// Fixed: Handle all edge cases including "0", "0.0", etc.
	if formattedRate == "" || formattedRate == "." || formattedRate == "-" ||
		formattedRate == "0." || formattedRate == ".0" || formattedRate == "-0" ||
		formattedRate == "0" || formattedRate == "0.0" {
		return "N/A"
	}

	// Handle numbers that start with decimal point
	if strings.HasPrefix(formattedRate, ".") {
		formattedRate = "0" + formattedRate
	}
	if strings.HasPrefix(formattedRate, "-.") {
		formattedRate = "-0" + formattedRate[1:]
	}

	return formattedRate
}
