package currency

import (
	"time"

	"golang.org/x/time/rate"
)

const (
	// API URLs
	whitebirdAPIURL   = "https://admin-service.whitebird.io/api/v1/exchange/calculation"
	bybitOrderbookURL = "https://api.bybit.com/v5/market/orderbook"
	mastercardAPIURL  = "https://www.mastercard.com/marketingservices/public/mccom-services/currency-conversions/conversion-rates"

	// Timeouts
	whitebirdAPITimeout = 15 * time.Second
	bybitAPITimeout     = 10 * time.Second
	mastercardTimeout   = 10 * time.Second
	requestTimeout      = 5 * time.Second

	// Update intervals
	backgroundUpdateTTL        = 5 * time.Minute
	criticalStalenessThreshold = 15 * time.Minute

	// Retry configuration
	maxRetries           = 3
	baseRetryDelay       = 1 * time.Second
	conversionMaxRetries = 3
	conversionRetryDelay = 100 * time.Millisecond

	// Trading fees (as decimals: 0.01 = 1%)
	feeBybitTrade             = 0.001
	feeUSDTToUSD              = 0.01
	feeUSDToUSDT              = 0.01
	feeMastercard             = 0.02
	feeTONWithdrawToBybit     = 0.0025
	feeTONWithdrawToWhitebird = 0.02

	// Order book thresholds
	minLargeOrderUSDT         = 1000.0
	slippageWarningThreshold  = 2.0
	liquidityToleranceStrict  = 0.95
	liquidityToleranceRelaxed = 0.90

	// Whitebird validation
	whitebirdRateMin   = 100.0
	whitebirdRateMax   = 300.0
	whitebirdMinSpread = 0.001
	whitebirdMaxSpread = 0.10

	// Scoring
	scoreSpecificConversion = 100
	scoreBaseConversion     = 90
	scoreReverseConversion  = 85
	scoreQuickConversion    = 80
	scoreInverseConversion  = 75
)

var (
	bybitLimiter      = rate.NewLimiter(rate.Every(time.Minute/100), 30)
	whitebirdLimiter  = rate.NewLimiter(rate.Every(time.Minute/60), 15)
	mastercardLimiter = rate.NewLimiter(rate.Every(time.Minute/30), 10)
)
