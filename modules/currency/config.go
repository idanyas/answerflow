package currency

import (
	"net"
	"net/http"
	"os"
	"time"

	"golang.org/x/time/rate"
)

// Currency code constants to prevent typos and improve maintainability
const (
	CurrencyRUB  = "RUB"
	CurrencyTON  = "TON"
	CurrencyUSD  = "USD"
	CurrencyUSDT = "USDT"
	CurrencyEUR  = "EUR"
)

// API URLs with environment variable override support
var (
	whitebirdAPIURL   = getEnvOrDefault("WHITEBIRD_API_URL", "https://admin-service.whitebird.io/api/v1/exchange/calculation")
	bybitOrderbookURL = getEnvOrDefault("BYBIT_ORDERBOOK_URL", "https://api.bybit.com/v5/market/orderbook")
	mastercardAPIURL  = getEnvOrDefault("MASTERCARD_API_URL", "https://www.mastercard.com/marketingservices/public/mccom-services/currency-conversions/conversion-rates")
)

// Timeouts
const (
	whitebirdAPITimeout        = 15 * time.Second
	bybitAPITimeout            = 10 * time.Second
	backgroundUpdateTTL        = 5 * time.Minute
	criticalStalenessThreshold = 15 * time.Minute
)

// Retry configuration
const (
	maxRetries     = 3
	baseRetryDelay = 1 * time.Second
	maxRetryDelay  = 10 * time.Second
)

// Trading fees
// IMPORTANT: Whitebird fee clarification
// The spec states 1.5% fee for RUB<->TON conversions.
// Empirical testing shows Whitebird applies approximately 2.4-2.5% effective fee.
// However, Whitebird's API already returns the final amount after fees in the 'outputAsset' field,
// so we do NOT apply additional fees in our code. We use the API response directly.
// For fee display, we show the spec value (1.5%) for consistency with documentation.
// The actual effective rate may differ and is handled internally by Whitebird's API.
const (
	// Bybit spot trading
	feeBybitTrade = 0.001 // 0.1%

	// Bybit card fiat conversion (both directions per spec)
	feeUSDTToUSD = 0.01 // 1%
	feeUSDToUSDT = 0.01 // 1%

	// Mastercard fiat conversion fee
	// Applied as multiplication: amount * rate * (1 - feeMastercard)
	// This correctly models the user receiving 2% less due to the fee
	feeMastercard = 0.02 // 2%

	// Whitebird fee is included in their API response, no additional fee applied here
	// Spec value: 1.5% (empirical observations show ~2.4-2.5%)

	// Blockchain transfer fees for TON
	feeTONWithdrawToBybit     = 0.0025 // Fixed TON fee to send from Whitebird to Bybit
	feeTONWithdrawToWhitebird = 0.02   // Fixed TON fee to withdraw from Bybit to Whitebird
)

// Order book thresholds
const (
	minLargeOrderUSDT         = 1000.0
	slippageWarningThreshold  = 2.0  // Warn if slippage exceeds 2%
	liquidityToleranceStrict  = 0.98 // Must fill 98% for large orders
	liquidityToleranceRelaxed = 0.95 // Must fill 95% for regular orders
)

// Validation
const (
	minAmountAfterFees  = 0.000001
	maxConversionAmount = 1e15

	// Input validation limits
	maxExpressionLength = 200
	maxQueryLength      = 500
	maxHTTPResponseSize = 5 * 1024 * 1024 // 5MB - sufficient for deep order books
)

// Scoring
const (
	scoreSpecificConversion = 100
	scoreBaseConversion     = 90
	scoreReverseConversion  = 85
	scoreQuickConversion    = 80
	scoreInverseConversion  = 75
)

// Cache settings
const (
	calculationCacheTTL = 2 * time.Minute
	maxCacheSize        = 10000
)

// Health monitoring
const (
	healthCheckInterval    = 1 * time.Minute
	maxConsecutiveFailures = 10
)

// Rate limiting
const (
	// API rate limiters
	bybitRatePerMinute      = 100
	bybitRateBurst          = 30
	whitebirdRatePerMinute  = 60
	whitebirdRateBurst      = 15
	mastercardRatePerMinute = 150 // Balanced rate with adaptive fetcher
	mastercardRateBurst     = 20  // Moderate burst
)

// Rate limiters
var (
	bybitLimiter      = rate.NewLimiter(rate.Every(time.Minute/bybitRatePerMinute), bybitRateBurst)
	whitebirdLimiter  = rate.NewLimiter(rate.Every(time.Minute/whitebirdRatePerMinute), whitebirdRateBurst)
	mastercardLimiter = rate.NewLimiter(rate.Every(time.Minute/mastercardRatePerMinute), mastercardRateBurst)
)

// Types
type BybitRate struct {
	BestBid       float64
	BestAsk       float64
	OrderBookBids [][]float64
	OrderBookAsks [][]float64
	LastUpdate    time.Time
}

type CurrencyMetadata struct {
	DecimalPlaces      int
	MinTradingAmount   float64
	MaxTradingAmount   float64
	IsTradeableOnBybit bool
	LastVerified       time.Time
}

// CreateHTTPClient creates an HTTP client with proper timeouts
func CreateHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   20,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}

// Helper function to get environment variable with default
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
