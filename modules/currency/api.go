package currency

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// Whitebird API
	whitebirdAPIURL     = "https://admin-service.whitebird.io/api/v1/exchange/calculation"
	whitebirdAPITimeout = 15 * time.Second

	// Bybit API
	bybitOrderbookURL = "https://api.bybit.com/v5/market/orderbook"
	bybitAPITimeout   = 10 * time.Second

	// Mastercard API
	mastercardAPIURL  = "https://www.mastercard.com/marketingservices/public/mccom-services/currency-conversions/conversion-rates"
	mastercardTimeout = 10 * time.Second

	// Background update interval - REDUCED for volatile crypto markets
	backgroundUpdateTTL = 5 * time.Minute

	// Staleness thresholds - REDUCED for better accuracy
	criticalStalenessThreshold = 15 * time.Minute // Force refresh if data is this old
	warningStalenessThreshold  = 5 * time.Minute  // Log warning if data is this old

	// Retry configuration
	maxRetries     = 3
	baseRetryDelay = 1 * time.Second

	// Order book liquidity tolerance
	liquidityTolerance = 0.95 // Allow 5% tolerance for matching

	// Rate limiting
	bybitRateLimit      = 100 // requests per minute
	whitebirdRateLimit  = 60  // requests per minute
	mastercardRateLimit = 30  // requests per minute
)

// Rate limiters for each API
var (
	bybitLimiter      = rate.NewLimiter(rate.Every(time.Minute/time.Duration(bybitRateLimit)), bybitRateLimit/10)
	whitebirdLimiter  = rate.NewLimiter(rate.Every(time.Minute/time.Duration(whitebirdRateLimit)), whitebirdRateLimit/10)
	mastercardLimiter = rate.NewLimiter(rate.Every(time.Minute/time.Duration(mastercardRateLimit)), mastercardRateLimit/10)
)

// CurrencyMetadata holds additional information about currencies
type CurrencyMetadata struct {
	DecimalPlaces      int
	MinTradingAmount   float64
	MaxTradingAmount   float64
	IsTradeableOnBybit bool
	LastVerified       time.Time
}

// BybitRate holds the best bid/ask and order book depth
type BybitRate struct {
	BestBid       float64     // Price to sell at (you receive USDT)
	BestAsk       float64     // Price to buy at (you pay USDT)
	OrderBookBids [][]float64 // Full bid side order book [[price, size], ...]
	OrderBookAsks [][]float64 // Full ask side order book [[price, size], ...]
	LastUpdate    time.Time
	Volume24h     float64 // 24h volume for determining large orders
}

// APICache holds all exchange rate data from multiple sources
type APICache struct {
	client *http.Client
	mu     sync.RWMutex

	// Whitebird: RUB <-> TON only
	whitebirdRates      map[string]float64 // "RUB_TON", "TON_RUB"
	whitebirdLastUpdate time.Time

	// Bybit: Crypto pairs (all vs USDT)
	bybitRates      map[string]*BybitRate // "TONUSDT", "BTCUSDT", etc.
	bybitLastUpdate time.Time

	// Mastercard: Fiat vs USD
	mastercardRates      map[string]float64 // "USD_EUR" -> rate, "USD_UAH" -> rate, etc.
	mastercardLastUpdate time.Time

	// Valid currency lists with metadata
	validCryptos map[string]bool
	validFiats   map[string]bool

	// Currency metadata
	currencyMetadata map[string]*CurrencyMetadata

	// Tradeable pairs cache
	tradeablePairs map[string]bool
	pairsLastCheck time.Time

	// Track last successful values for comparison
	lastWhitebirdRates  map[string]float64
	lastBybitRates      map[string]*BybitRate
	lastMastercardRates map[string]float64
}

// Crypto currencies supported on Bybit
var supportedCryptos = []string{
	"BTC", "ETH", "XRP", "DOT", "XLM", "LTC", "DOGE", "CHZ", "AXS", "MANA", "DYDX", "COMP", "AAVE", "YFI",
	"LINK", "SUSHI", "UNI", "KSM", "ICP", "ADA", "ETC", "XTZ", "BCH", "QNT", "USDC", "GRT", "SOL", "FIL",
	"BAT", "ZRX", "CRV", "AGLD", "ANKR", "PERP", "WAVES", "LUNC", "SPELL", "SHIB", "ATOM", "ALGO", "ENJ",
	"SAND", "AVAX", "WOO", "FTT", "GODS", "IMX", "ENS", "CAKE", "STETH", "SLP", "C98", "AVA", "ONE", "BOBA",
	"JASMY", "GALA", "TRVL", "WEMIX", "XEM", "BICO", "UMA", "NEXO", "SNX", "1INCH", "TEL", "SIS", "LRC",
	"LDO", "IZI", "QTUM", "ZEN", "THETA", "MX", "DGB", "RVN", "EGLD", "RUNE", "XEC", "ICX", "XDC", "HNT",
	"ZIL", "HBAR", "FLOW", "KASTA", "STX", "SIDUS", "LOOKS", "DAI", "MV", "RSS3", "GMX", "ACH", "JST",
	"SUN", "BTT", "TRX", "NFT", "SCRT", "PSTAKE", "USTC", "BNB", "NEAR", "SD", "APE", "FIDA", "MINA",
	"SC", "RACA", "GLMR", "MOVR", "WBTC", "XAVA", "GMT", "CELO", "SFUND", "APEX", "CTC", "FITFI", "USDD",
	"OP", "LUNA", "VINU", "BEL", "FORT", "FLOKI", "BABYDOGE", "WAXP", "AR", "ROSE", "PSG", "JUV", "INTER",
	"AFC", "CITY", "SOLO", "SWEAT", "ETHW", "INJ", "MPLX", "APT", "MCRT", "MASK", "HFT", "PEOPLE", "TWT",
	"ORT", "HOOK", "OAS", "MAGIC", "MEE", "TON", "BONK", "FLR", "TIME", "RPL", "SSV", "FXS", "CORE", "RDNT",
	"BLUR", "MDAO", "ACS", "PRIME", "VRA", "ID", "ARB", "XCAD", "MBX", "AXL", "CGPT", "AGI", "SUI", "MVL",
	"PEPE", "LADYS", "LMWR", "TURBOS", "VELO", "PENDLE", "NYM", "MNT", "ARKM", "NEON", "WLD", "SEI",
	"CYBER", "ORDI", "KAVA", "PYUSD", "KAS", "FET", "ZTX", "JEFF", "TUSD", "BEAM", "POL", "TIA", "TOKEN",
	"MEME", "SHRAP", "FLIP", "ROOT", "PYTH", "KUB", "KCS", "VANRY", "INSP", "JTO", "METH", "CBK", "ZIG",
	"TRC", "MYRIA", "MBOX", "ARTY", "COQ", "AIOZ", "VIC", "RATS", "SATS", "PORT3", "XAI", "ONDO", "SQR",
	"SAROS", "USDY", "MANTA", "MYRO", "GTAI", "DMAIL", "DYM", "ZETA", "JUP", "MAVIA", "PURSE", "ALT",
	"HTX", "CSPR", "STRK", "CPOOL", "QORPO", "PORTAL", "SCA", "AEVO", "NIBI", "BOME", "VENOM", "ZKJ",
	"ETHFI", "NAKA", "WEN", "DEGEN", "ENA", "USDE", "W", "G3", "ESE", "TNSR", "MASA", "FOXY", "PRCL",
	"BRETT", "MEW", "MERL", "LL", "SAFE", "WIF", "SVL", "KMNO", "ZENT", "TAI", "MODE", "SPEC", "PONKE",
	"BB", "CTA", "NOT", "DRIFT", "SQD", "MONPRO", "MOG", "TAIKO", "ULTI", "AURORA", "IO", "ATH", "COOKIE",
	"PIRATE", "ZK", "POPCAT", "ZRO", "NRN", "ZEX", "BLAST", "MOCA", "UXLINK", "A8", "CLOUD", "ZKL", "AVAIL",
	"L3", "RENDER", "G", "DOGS", "ORDER", "SUNDOG", "CATI", "HMSTR", "EIGEN", "NAVX", "BBSOL", "CARV",
	"DEEP", "PUFFER", "DBR", "PUFF", "SCR", "X", "COOK", "GRASS", "KAIA", "SWELL", "NS", "GOAT", "CMETH",
	"MORPHO", "NEIROCTO", "BAN", "OL", "VIRTUAL", "MAJOR", "MEMEFI", "PNUT", "SPX", "ZRC", "CHILLGUY",
	"SUPRA", "PAAL", "F", "HPOS10I", "XION", "MOVE", "ME", "ZEREBRO", "SEND", "AERO", "STREAM", "VANA",
	"LUNAI", "PENGU", "FLUID", "FUEL", "ODOS", "ALCH", "FLOCK", "SONIC", "SERAPH", "LAVA", "XTER", "GAME",
	"AIXBT", "J", "S", "BMT", "TOSHI", "GPS", "SOLV", "OBT", "SOSO", "TRUMP", "PLUME", "ANIME", "LAYER",
	"PINEYE", "BERA", "AVL", "B3", "DIAM", "IP", "OM", "USDTB", "ROAM", "ELX", "RED", "PELL", "BR", "VVV",
	"USDQ", "WAL", "AMI", "PARTI", "CORN", "KILO", "PUMPBTC", "USDR", "FHE", "BABY1", "XAUT", "VET", "VTHO",
	"WCT", "EPT", "DOLO", "HYPER", "ZORA", "INIT", "SIGN", "HAEDAL", "MILK", "OBOL", "SXT", "DOOD", "NXPC",
	"HUMA", "ELDE", "AO", "A", "ASRR", "BDXN", "LA", "CUDIS", "RESOLV", "SKATE", "HOME", "BOMB", "SPK",
	"H", "NEWT", "XO", "SAHARA", "ICNT", "FRAG", "NVDAX", "COINX", "AAPLX", "CRCLX", "METAX", "HOODX",
	"AMZNX", "GOOGLX", "USD1", "MCDX", "TSLAX", "HYPE", "TAC", "PUMP", "ES", "ERA", "TA", "CAT", "TREE",
	"TUNA", "TOWNS", "PROVE", "K", "CAMP", "CFG", "SHARDS", "WLFI", "SOMI", "SKY", "U", "AVNT", "ART",
	"LINEA", "XUSD", "HOLO", "ZKC", "PORTALS", "BARD", "LBTC", "ASTER", "XPL", "0G", "XAN", "RLUSD", "FF",
	"2Z", "NOM", "ENSO", "YB", "RECALL", "ZBT", "MET", "COMMON", "SYND", "EAT", "MMT", "LITKEY", "CC",
	"USDT", // Include USDT as a crypto
}

// Fiat currencies supported by Mastercard
var supportedFiats = []string{
	"AFN", "ALL", "DZD", "AOA", "ARS", "AMD", "AWG", "AUD", "AZN", "BSD", "BHD", "BDT", "BBD", "BYN", "BZD",
	"BMD", "BTN", "BOB", "BAM", "BWP", "BRL", "BND", "BGN", "BIF", "KHR", "CAD", "CVE", "XCG", "KYD", "XOF",
	"XAF", "XPF", "CLP", "CNY", "COP", "KMF", "CDF", "CRC", "CUP", "CZK", "DKK", "DJF", "DOP", "XCD", "EGP",
	"SVC", "ETB", "EUR", "FKP", "FJD", "GMD", "GEL", "GHS", "GIP", "GBP", "GTQ", "GNF", "GYD", "HTG", "HNL",
	"HKD", "HUF", "ISK", "INR", "IDR", "IQD", "ILS", "JMD", "JPY", "JOD", "KZT", "KES", "KWD", "KGS", "LAK",
	"LBP", "LSL", "LRD", "LYD", "MOP", "MKD", "MGA", "MWK", "MYR", "MVR", "MRU", "MUR", "MXN", "MDL", "MNT",
	"MAD", "MZN", "MMK", "NAD", "NPR", "NZD", "NIO", "NGN", "NOK", "OMR", "PKR", "PAB", "PGK", "PYG", "PEN",
	"PHP", "PLN", "QAR", "RON", "RWF", "SHP", "WST", "STN", "SAR", "RSD", "SCR", "SLE", "SGD", "SBD", "SOS",
	"ZAR", "KRW", "SSP", "LKR", "SDG", "SRD", "SZL", "SEK", "CHF", "TWD", "TJS", "TZS", "THB", "TOP", "TTD",
	"TND", "TRY", "TMT", "UGX", "UAH", "AED", "USD", "UYU", "UZS", "VUV", "VES", "VND", "YER", "ZMW", "ZWG",
	"RUB", // Add RUB to fiat list
}

// Priority fiat currencies for initial fetch
var priorityFiats = []string{"EUR", "GBP", "JPY", "CNY", "CHF", "CAD", "AUD", "UAH", "TRY", "KRW"}

// NewAPICache creates a new API cache
func NewAPICache() *APICache {
	validCryptos := make(map[string]bool)
	for _, c := range supportedCryptos {
		validCryptos[c] = true
	}

	validFiats := make(map[string]bool)
	for _, f := range supportedFiats {
		validFiats[f] = true
	}

	return &APICache{
		client:              &http.Client{Timeout: 30 * time.Second},
		whitebirdRates:      make(map[string]float64),
		bybitRates:          make(map[string]*BybitRate),
		mastercardRates:     make(map[string]float64),
		validCryptos:        validCryptos,
		validFiats:          validFiats,
		currencyMetadata:    make(map[string]*CurrencyMetadata),
		tradeablePairs:      make(map[string]bool),
		lastWhitebirdRates:  make(map[string]float64),
		lastBybitRates:      make(map[string]*BybitRate),
		lastMastercardRates: make(map[string]float64),
	}
}

// IsStale checks if cached data is stale
func (ac *APICache) IsStale() bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	now := time.Now()

	// Check each cache's staleness with reduced thresholds
	if now.Sub(ac.whitebirdLastUpdate) > criticalStalenessThreshold {
		return true
	}
	if now.Sub(ac.bybitLastUpdate) > criticalStalenessThreshold {
		return true
	}
	// Mastercard can be more stale as rates change less frequently
	if now.Sub(ac.mastercardLastUpdate) > criticalStalenessThreshold*4 {
		return true
	}

	return false
}

// IsTradeablePair checks if a pair is tradeable on Bybit
func (ac *APICache) IsTradeablePair(symbol string) bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	// Check if we need to refresh tradeable pairs cache
	if time.Since(ac.pairsLastCheck) > time.Hour {
		// Return existing value, trigger background refresh
		go ac.refreshTradeablePairs()
	}

	return ac.tradeablePairs[symbol]
}

// refreshTradeablePairs updates the list of tradeable pairs
func (ac *APICache) refreshTradeablePairs() {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	// Update from current bybit rates
	for symbol := range ac.bybitRates {
		ac.tradeablePairs[symbol] = true
	}
	ac.pairsLastCheck = time.Now()
}

// GetCurrencyMetadata returns metadata for a currency
func (ac *APICache) GetCurrencyMetadata(code string) *CurrencyMetadata {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	if meta, ok := ac.currencyMetadata[code]; ok {
		return meta
	}

	// Return default metadata
	decimalPlaces := 2
	switch code {
	case "BTC":
		decimalPlaces = 8
	case "ETH", "TON":
		decimalPlaces = 6
	}

	return &CurrencyMetadata{
		DecimalPlaces:    decimalPlaces,
		MinTradingAmount: 0.000001,
		MaxTradingAmount: 1000000,
	}
}

// InitialFetch performs initial data loading
func (ac *APICache) InitialFetch() error {
	var wg sync.WaitGroup
	var errWhitebird, errBybit, errMastercard error

	wg.Add(3)

	go func() {
		defer wg.Done()
		errWhitebird = ac.fetchWhitebirdRates()
	}()

	go func() {
		defer wg.Done()
		errBybit = ac.fetchBybitRates()
	}()

	go func() {
		defer wg.Done()
		errMastercard = ac.fetchMastercardRates()
	}()

	wg.Wait()

	if errWhitebird != nil {
		log.Printf("Warning: Whitebird fetch failed: %v", errWhitebird)
	}
	if errBybit != nil {
		log.Printf("Warning: Bybit fetch failed: %v", errBybit)
	}
	if errMastercard != nil {
		log.Printf("Warning: Mastercard fetch failed: %v", errMastercard)
	}

	// Require at least Whitebird and Bybit to succeed
	if errWhitebird != nil && errBybit != nil {
		return fmt.Errorf("critical providers failed: whitebird=%v, bybit=%v", errWhitebird, errBybit)
	}

	// Initialize tradeable pairs
	ac.refreshTradeablePairs()

	return nil
}

// StartBackgroundUpdaters launches background update loops
func (ac *APICache) StartBackgroundUpdaters() {
	log.Println("Starting background currency updaters with reduced intervals...")
	go ac.updateWhitebirdLoop()
	go ac.updateBybitLoop()
	go ac.updateMastercardLoop()
}

// retryWithBackoff implements exponential backoff retry logic
func retryWithBackoff(fn func() error) error {
	var lastErr error
	delay := baseRetryDelay

	for i := 0; i < maxRetries; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
			if i < maxRetries-1 {
				time.Sleep(delay)
				delay *= 2 // Exponential backoff
			}
		}
	}

	return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// Background update loops with reduced intervals
func (ac *APICache) updateWhitebirdLoop() {
	ticker := time.NewTicker(backgroundUpdateTTL)
	defer ticker.Stop()
	for range ticker.C {
		if err := retryWithBackoff(ac.fetchWhitebirdRates); err != nil {
			log.Printf("ERROR: Whitebird background update failed: %v", err)
			// Check staleness
			ac.mu.RLock()
			staleness := time.Since(ac.whitebirdLastUpdate)
			ac.mu.RUnlock()
			if staleness > warningStalenessThreshold {
				log.Printf("CRITICAL: Whitebird data is %v old", staleness)
			}
		}
	}
}

func (ac *APICache) updateBybitLoop() {
	ticker := time.NewTicker(backgroundUpdateTTL)
	defer ticker.Stop()
	for range ticker.C {
		if err := retryWithBackoff(ac.fetchBybitRates); err != nil {
			log.Printf("ERROR: Bybit background update failed: %v", err)
			ac.mu.RLock()
			staleness := time.Since(ac.bybitLastUpdate)
			ac.mu.RUnlock()
			if staleness > warningStalenessThreshold {
				log.Printf("CRITICAL: Bybit data is %v old", staleness)
			}
		}
	}
}

func (ac *APICache) updateMastercardLoop() {
	ticker := time.NewTicker(backgroundUpdateTTL * 3) // Less frequent for fiat
	defer ticker.Stop()
	for range ticker.C {
		if err := retryWithBackoff(ac.fetchMastercardRates); err != nil {
			log.Printf("ERROR: Mastercard background update failed: %v", err)
			ac.mu.RLock()
			staleness := time.Since(ac.mastercardLastUpdate)
			ac.mu.RUnlock()
			if staleness > warningStalenessThreshold*4 { // More lenient for Mastercard
				log.Printf("WARNING: Mastercard data is %v old", staleness)
			}
		}
	}
}

// Whitebird API fetch
type whitebirdRequestPayload struct {
	CurrencyPair whitebirdCurrencyPair `json:"currencyPair"`
	Calculation  whitebirdCalculation  `json:"calculation"`
}

type whitebirdCurrencyPair struct {
	FromCurrency string `json:"fromCurrency"`
	ToCurrency   string `json:"toCurrency"`
}

type whitebirdCalculation struct {
	InputAsset float64 `json:"inputAsset"`
}

type whitebirdResponse struct {
	Rate struct {
		PlainRatio string `json:"plainRatio"` // Rate without fees
		Ratio      string `json:"ratio"`      // Rate with fees included - THIS IS WHAT WE USE
	} `json:"rate"`
	Calculation struct {
		OutputAsset string `json:"outputAsset"` // Amount received after fees
	} `json:"calculation"`
}

func (ac *APICache) fetchWhitebirdRates() error {
	// Apply rate limiting
	if err := whitebirdLimiter.Wait(context.Background()); err != nil {
		return fmt.Errorf("rate limit error: %w", err)
	}

	log.Println("Fetching Whitebird rates...")
	ctx, cancel := context.WithTimeout(context.Background(), whitebirdAPITimeout)
	defer cancel()

	pairs := []struct{ from, to string }{
		{"RUB", "TON"},
		{"TON", "RUB"},
	}

	fetchedRates := make(map[string]float64)

	for _, pair := range pairs {
		rate, err := ac.fetchSingleWhitebirdRate(ctx, pair.from, pair.to)
		if err != nil {
			log.Printf("Warning: Failed to fetch Whitebird %s->%s: %v", pair.from, pair.to, err)
			continue
		}
		cacheKey := fmt.Sprintf("%s_%s", pair.from, pair.to)
		fetchedRates[cacheKey] = rate
		time.Sleep(200 * time.Millisecond)
	}

	if len(fetchedRates) == 0 {
		return fmt.Errorf("all Whitebird rate fetches failed")
	}

	// Only update if rates have actually changed
	hasChanges := false
	for key, rate := range fetchedRates {
		if oldRate, ok := ac.lastWhitebirdRates[key]; !ok || math.Abs(oldRate-rate) > 0.00001 {
			hasChanges = true
			break
		}
	}

	if hasChanges {
		ac.mu.Lock()
		for key, rate := range fetchedRates {
			ac.whitebirdRates[key] = rate
			ac.lastWhitebirdRates[key] = rate
		}
		ac.whitebirdLastUpdate = time.Now()
		ac.mu.Unlock()
		log.Printf("Whitebird rates updated successfully: %d rates", len(fetchedRates))
	}

	return nil
}

func (ac *APICache) fetchSingleWhitebirdRate(ctx context.Context, from, to string) (float64, error) {
	// Apply rate limiting
	if err := whitebirdLimiter.Wait(ctx); err != nil {
		return 0, fmt.Errorf("rate limit error: %w", err)
	}

	payload := whitebirdRequestPayload{
		CurrencyPair: whitebirdCurrencyPair{
			FromCurrency: from,
			ToCurrency:   to,
		},
		Calculation: whitebirdCalculation{InputAsset: 1},
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshaling payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", whitebirdAPIURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://whitebird.io")

	resp, err := ac.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("API returned status %s", resp.Status)
	}

	var wbResp whitebirdResponse
	if err := json.NewDecoder(resp.Body).Decode(&wbResp); err != nil {
		return 0, fmt.Errorf("decoding response: %w", err)
	}

	// Use the Ratio field which ALREADY INCLUDES the 1.5% fee
	rate, err := strconv.ParseFloat(wbResp.Rate.Ratio, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing rate '%s': %w", wbResp.Rate.Ratio, err)
	}

	return rate, nil
}

// Bybit API fetch with order book depth and volume data
func (ac *APICache) fetchBybitRates() error {
	log.Println("Fetching Bybit rates...")
	ctx, cancel := context.WithTimeout(context.Background(), bybitAPITimeout*10)
	defer cancel()

	fetchedRates := make(map[string]*BybitRate)
	var mu sync.Mutex

	// Fetch only important pairs first
	keyPairs := []string{"TONUSDT", "BTCUSDT", "ETHUSDT", "SOLUSDT", "ADAUSDT", "DOGEUSDT"}

	// Then add other major cryptos
	majorCryptos := []string{"XRP", "DOT", "LINK", "UNI", "ATOM", "AVAX", "NEAR", "APT", "ARB", "OP"}
	for _, crypto := range majorCryptos {
		symbol := crypto + "USDT"
		if !contains(keyPairs, symbol) {
			keyPairs = append(keyPairs, symbol)
		}
	}

	// Limit concurrent requests
	sem := make(chan struct{}, 5) // Reduced concurrency
	var wg sync.WaitGroup

	for _, symbol := range keyPairs {
		wg.Add(1)
		go func(sym string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			rate, err := ac.fetchBybitOrderbook(ctx, sym)
			if err != nil {
				// Silent fail for non-existent pairs
				return
			}
			mu.Lock()
			fetchedRates[sym] = rate
			mu.Unlock()
		}(symbol)
		time.Sleep(100 * time.Millisecond) // Increased delay
	}

	wg.Wait()

	// Update metadata for fetched currencies
	ac.mu.Lock()
	for key, rate := range fetchedRates {
		ac.bybitRates[key] = rate
		ac.lastBybitRates[key] = rate
		// Mark as tradeable
		ac.tradeablePairs[key] = true

		// Extract crypto code (remove USDT suffix)
		if len(key) > 4 && key[len(key)-4:] == "USDT" {
			cryptoCode := key[:len(key)-4]
			ac.currencyMetadata[cryptoCode] = &CurrencyMetadata{
				DecimalPlaces:      getDecimalPlaces(cryptoCode),
				MinTradingAmount:   0.000001,
				MaxTradingAmount:   1000000,
				IsTradeableOnBybit: true,
				LastVerified:       time.Now(),
			}
		}
	}
	ac.bybitLastUpdate = time.Now()
	ac.pairsLastCheck = time.Now()
	ac.mu.Unlock()

	log.Printf("Bybit rates updated: %d pairs", len(fetchedRates))
	return nil
}

// Fixed: Optimize order book fetching based on order size
func (ac *APICache) fetchBybitOrderbook(ctx context.Context, symbol string) (*BybitRate, error) {
	// Apply rate limiting
	if err := bybitLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit error: %w", err)
	}

	// First get ticker for 24h volume
	tickerURL := fmt.Sprintf("https://api.bybit.com/v5/market/tickers?category=spot&symbol=%s", symbol)
	tickerReq, err := http.NewRequestWithContext(ctx, "GET", tickerURL, nil)
	if err != nil {
		return nil, err
	}

	tickerResp, err := ac.client.Do(tickerReq)
	if err != nil {
		return nil, err
	}
	defer tickerResp.Body.Close()

	var tickerResult struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []struct {
				Volume24h string `json:"volume24h"`
			} `json:"list"`
		} `json:"result"`
	}

	volume24h := 0.0
	if err := json.NewDecoder(tickerResp.Body).Decode(&tickerResult); err == nil && len(tickerResult.Result.List) > 0 {
		volume24h, _ = strconv.ParseFloat(tickerResult.Result.List[0].Volume24h, 64)
	}

	// Fixed: Fetch appropriate depth based on typical order sizes
	// For most orders, 50 levels is sufficient
	limit := 50
	// Only fetch deep order book for major pairs with high volume
	if volume24h > 10000000 && (symbol == "BTCUSDT" || symbol == "ETHUSDT" || symbol == "TONUSDT") {
		limit = 100
	}

	url := fmt.Sprintf("%s?category=spot&symbol=%s&limit=%d", bybitOrderbookURL, symbol, limit)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := ac.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}

	var result struct {
		RetCode int `json:"retCode"`
		Result  struct {
			A [][]string `json:"a"` // asks [[price, size], ...]
			B [][]string `json:"b"` // bids [[price, size], ...]
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if result.RetCode != 0 || len(result.Result.A) == 0 || len(result.Result.B) == 0 {
		return nil, fmt.Errorf("invalid response")
	}

	// Parse order book into float arrays
	orderBookAsks := make([][]float64, 0, len(result.Result.A))
	for _, ask := range result.Result.A {
		if len(ask) >= 2 {
			price, _ := strconv.ParseFloat(ask[0], 64)
			size, _ := strconv.ParseFloat(ask[1], 64)
			if price > 0 && size > 0 {
				orderBookAsks = append(orderBookAsks, []float64{price, size})
			}
		}
	}

	orderBookBids := make([][]float64, 0, len(result.Result.B))
	for _, bid := range result.Result.B {
		if len(bid) >= 2 {
			price, _ := strconv.ParseFloat(bid[0], 64)
			size, _ := strconv.ParseFloat(bid[1], 64)
			if price > 0 && size > 0 {
				orderBookBids = append(orderBookBids, []float64{price, size})
			}
		}
	}

	if len(orderBookAsks) == 0 || len(orderBookBids) == 0 {
		return nil, fmt.Errorf("empty order book")
	}

	return &BybitRate{
		BestBid:       orderBookBids[0][0],
		BestAsk:       orderBookAsks[0][0],
		OrderBookBids: orderBookBids,
		OrderBookAsks: orderBookAsks,
		LastUpdate:    time.Now(),
		Volume24h:     volume24h,
	}, nil
}

// CalculateAverageExecutionPrice calculates the average price for executing a trade of given size
func (ac *APICache) CalculateAverageExecutionPrice(symbol string, amount float64, isBuy bool) (float64, error) {
	ac.mu.RLock()
	rate, ok := ac.bybitRates[symbol]
	ac.mu.RUnlock()

	if !ok || rate == nil {
		return 0, fmt.Errorf("rate not available for %s", symbol)
	}

	var orderBook [][]float64
	if isBuy {
		orderBook = rate.OrderBookAsks
	} else {
		orderBook = rate.OrderBookBids
	}

	if len(orderBook) == 0 {
		return 0, fmt.Errorf("empty order book for %s", symbol)
	}

	totalFilled := 0.0
	totalCost := 0.0

	for _, level := range orderBook {
		price := level[0]
		size := level[1]

		if totalFilled+size <= amount {
			// Can fill entire level
			totalFilled += size
			totalCost += price * size
		} else {
			// Partial fill of this level
			remaining := amount - totalFilled
			totalCost += price * remaining
			totalFilled = amount
			break
		}

		if totalFilled >= amount {
			break
		}
	}

	// Use 5% tolerance for liquidity
	if totalFilled < amount*liquidityTolerance {
		return 0, fmt.Errorf("insufficient liquidity: only %.2f of %.2f available", totalFilled, amount)
	}

	if totalFilled == 0 {
		return 0, fmt.Errorf("no liquidity available")
	}

	averagePrice := totalCost / totalFilled
	return averagePrice, nil
}

// NEW FUNCTION: CalculateBuyAmountWithUSDT calculates how much crypto can be bought with a specific USDT amount
func (ac *APICache) CalculateBuyAmountWithUSDT(symbol string, usdtAmount float64) (cryptoAmount float64, avgPrice float64, err error) {
	ac.mu.RLock()
	rate, ok := ac.bybitRates[symbol]
	ac.mu.RUnlock()

	if !ok || rate == nil {
		return 0, 0, fmt.Errorf("rate not available for %s", symbol)
	}

	orderBook := rate.OrderBookAsks // We're buying, so we look at asks
	if len(orderBook) == 0 {
		return 0, 0, fmt.Errorf("empty ask order book for %s", symbol)
	}

	totalUSDTSpent := 0.0
	totalCryptoReceived := 0.0

	for _, level := range orderBook {
		price := level[0]
		size := level[1]
		levelCostUSDT := price * size

		if totalUSDTSpent+levelCostUSDT <= usdtAmount {
			// Can buy entire level
			totalUSDTSpent += levelCostUSDT
			totalCryptoReceived += size
		} else {
			// Partial fill of this level
			remainingUSDT := usdtAmount - totalUSDTSpent
			partialCrypto := remainingUSDT / price
			totalCryptoReceived += partialCrypto
			totalUSDTSpent = usdtAmount
			break
		}

		if totalUSDTSpent >= usdtAmount {
			break
		}
	}

	// Check if we could spend at least 95% of the USDT
	if totalUSDTSpent < usdtAmount*liquidityTolerance {
		// Try to use whatever we could get
		if totalCryptoReceived > 0 {
			avgPrice = totalUSDTSpent / totalCryptoReceived
			return totalCryptoReceived, avgPrice, nil
		}
		return 0, 0, fmt.Errorf("insufficient liquidity: only %.2f USDT of %.2f could be spent", totalUSDTSpent, usdtAmount)
	}

	if totalCryptoReceived == 0 {
		return 0, 0, fmt.Errorf("no liquidity available")
	}

	avgPrice = totalUSDTSpent / totalCryptoReceived
	return totalCryptoReceived, avgPrice, nil
}

// Mastercard API fetch - optimized to fetch only priority currencies
func (ac *APICache) fetchMastercardRates() error {
	log.Println("Fetching Mastercard rates...")
	ctx, cancel := context.WithTimeout(context.Background(), mastercardTimeout*time.Duration(len(priorityFiats)))
	defer cancel()

	fetchedRates := make(map[string]float64)
	var mu sync.Mutex

	// Limit concurrent requests
	sem := make(chan struct{}, 3) // Reduced concurrency
	var wg sync.WaitGroup

	// Fetch only priority currencies
	for _, fiat := range priorityFiats {
		if fiat == "USD" || fiat == "RUB" {
			continue
		}

		wg.Add(1)
		go func(targetFiat string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			rate, err := ac.fetchMastercardRate(ctx, "USD", targetFiat)
			if err != nil {
				// Silent fail for less important currencies
				return
			}
			mu.Lock()
			fetchedRates[fmt.Sprintf("USD_%s", targetFiat)] = rate
			mu.Unlock()
		}(fiat)
		time.Sleep(200 * time.Millisecond) // Increased delay
	}

	wg.Wait()

	// Only update if rates have changed
	hasChanges := false
	for key, rate := range fetchedRates {
		if oldRate, ok := ac.lastMastercardRates[key]; !ok || math.Abs(oldRate-rate)/oldRate > 0.0001 {
			hasChanges = true
			break
		}
	}

	if hasChanges {
		ac.mu.Lock()
		for key, rate := range fetchedRates {
			ac.mastercardRates[key] = rate
			ac.lastMastercardRates[key] = rate
		}
		ac.mastercardLastUpdate = time.Now()
		ac.mu.Unlock()
		log.Printf("Mastercard rates updated: %d pairs", len(fetchedRates))
	}

	return nil
}

// Fixed: Add all required Mastercard headers
func (ac *APICache) fetchMastercardRate(ctx context.Context, from, to string) (float64, error) {
	// Apply rate limiting
	if err := mastercardLimiter.Wait(ctx); err != nil {
		return 0, fmt.Errorf("rate limit error: %w", err)
	}

	url := fmt.Sprintf("%s?exchange_date=0000-00-00&transaction_currency=%s&cardholder_billing_currency=%s&bank_fee=0&transaction_amount=10000000",
		mastercardAPIURL, from, to)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}

	// Fixed: Add all required headers from examples
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:144.0) Gecko/20100101 Firefox/144.0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Referer", "https://www.mastercard.com/global/en/personal/get-support/currency-exchange-rate-converter.html")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Priority", "u=0")
	req.Header.Set("TE", "trailers")

	resp, err := ac.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %s", resp.Status)
	}

	var result struct {
		Data struct {
			ConversionRate string `json:"conversionRate"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	rate, err := strconv.ParseFloat(result.Data.ConversionRate, 64)
	if err != nil || rate <= 0 {
		return 0, fmt.Errorf("invalid rate: %s", result.Data.ConversionRate)
	}

	return rate, nil
}

// Public accessors
func (ac *APICache) GetWhitebirdRate(from, to string) (float64, error) {
	key := fmt.Sprintf("%s_%s", from, to)
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	rate, ok := ac.whitebirdRates[key]
	if !ok || rate <= 0 {
		return 0, fmt.Errorf("rate not available for %s", key)
	}
	return rate, nil
}

func (ac *APICache) GetBybitRate(symbol string) (*BybitRate, error) {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	rate, ok := ac.bybitRates[symbol]
	if !ok || rate == nil {
		return nil, fmt.Errorf("rate not available for %s", symbol)
	}
	return rate, nil
}

// GetBybitRateForAmount gets the effective rate for a specific amount considering order book depth
func (ac *APICache) GetBybitRateForAmount(symbol string, amount float64, isBuy bool) (float64, error) {
	avgPrice, err := ac.CalculateAverageExecutionPrice(symbol, amount, isBuy)
	if err != nil {
		// Fallback to best bid/ask if order book calculation fails
		ac.mu.RLock()
		rate, ok := ac.bybitRates[symbol]
		ac.mu.RUnlock()

		if !ok || rate == nil {
			return 0, fmt.Errorf("rate not available for %s", symbol)
		}

		if isBuy {
			return rate.BestAsk, nil
		}
		return rate.BestBid, nil
	}

	return avgPrice, nil
}

func (ac *APICache) GetMastercardRate(from, to string) (float64, error) {
	if from == to {
		return 1.0, nil
	}

	// Handle USD base currency
	if from == "USD" {
		key := fmt.Sprintf("%s_%s", from, to)
		ac.mu.RLock()
		defer ac.mu.RUnlock()

		rate, ok := ac.mastercardRates[key]
		if !ok || rate <= 0 {
			return 0, fmt.Errorf("rate not available for %s", key)
		}
		return rate, nil
	}

	// Handle reverse rates
	if to == "USD" {
		key := fmt.Sprintf("USD_%s", from)
		ac.mu.RLock()
		defer ac.mu.RUnlock()

		rate, ok := ac.mastercardRates[key]
		if !ok || rate <= 0 {
			return 0, fmt.Errorf("rate not available for %s", key)
		}
		return 1.0 / rate, nil
	}

	// Cross rates via USD
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	fromKey := fmt.Sprintf("USD_%s", from)
	toKey := fmt.Sprintf("USD_%s", to)

	fromRate, okFrom := ac.mastercardRates[fromKey]
	toRate, okTo := ac.mastercardRates[toKey]

	if !okFrom || !okTo || fromRate <= 0 || toRate <= 0 {
		return 0, fmt.Errorf("cross rate not available for %s to %s", from, to)
	}

	return toRate / fromRate, nil
}

func (ac *APICache) IsCrypto(code string) bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	return ac.validCryptos[code]
}

func (ac *APICache) IsFiat(code string) bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	return ac.validFiats[code]
}

// ForceRefresh triggers an immediate refresh of all rates
func (ac *APICache) ForceRefresh() error {
	log.Println("Force refreshing all rates...")
	var wg sync.WaitGroup
	var errWhitebird, errBybit error

	wg.Add(3)

	go func() {
		defer wg.Done()
		errWhitebird = retryWithBackoff(ac.fetchWhitebirdRates)
	}()

	go func() {
		defer wg.Done()
		errBybit = retryWithBackoff(ac.fetchBybitRates)
	}()

	go func() {
		defer wg.Done()
		// Mastercard refresh is optional
		_ = retryWithBackoff(ac.fetchMastercardRates)
	}()

	wg.Wait()

	if errWhitebird != nil && errBybit != nil {
		return fmt.Errorf("critical providers failed during force refresh")
	}

	return nil
}

// GetCacheStaleness returns how old the cached data is for each provider
func (ac *APICache) GetCacheStaleness() map[string]time.Duration {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	now := time.Now()
	return map[string]time.Duration{
		"whitebird":  now.Sub(ac.whitebirdLastUpdate),
		"bybit":      now.Sub(ac.bybitLastUpdate),
		"mastercard": now.Sub(ac.mastercardLastUpdate),
	}
}

// Fixed: CalculateSlippage now returns percentage directly
func (ac *APICache) CalculateSlippage(symbol string, amount float64, isBuy bool) (float64, error) {
	avgPrice, err := ac.CalculateAverageExecutionPrice(symbol, amount, isBuy)
	if err != nil {
		return 0, err
	}

	ac.mu.RLock()
	rate, ok := ac.bybitRates[symbol]
	ac.mu.RUnlock()

	if !ok || rate == nil {
		return 0, fmt.Errorf("rate not available for %s", symbol)
	}

	var bestPrice float64
	if isBuy {
		bestPrice = rate.BestAsk
	} else {
		bestPrice = rate.BestBid
	}

	if bestPrice == 0 {
		return 0, fmt.Errorf("invalid best price")
	}

	// Fixed: Return as percentage directly (no multiplication by 100)
	slippage := math.Abs((avgPrice-bestPrice)/bestPrice) * 100
	return slippage, nil
}

// Helper functions
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func getDecimalPlaces(cryptoCode string) int {
	switch cryptoCode {
	case "BTC":
		return 8
	case "ETH", "TON":
		return 6
	case "SHIB", "PEPE":
		return 0
	default:
		return 2
	}
}
