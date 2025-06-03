package currency // UPDATED package declaration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
)

const (
	allCurrenciesURL = "https://cdn.jsdelivr.net/npm/@fawazahmed0/currency-api@latest/v1/currencies.json"
	ratesBaseURL     = "https://cdn.jsdelivr.net/npm/@fawazahmed0/currency-api@latest/v1/currencies/%s.json" // %s is lowercase base currency
	apiTimeout       = 4 * time.Second

	// Bybit P2P constants
	bybitP2PAPIURL          = "https://api2.bybit.com/fiat/otc/item/online"
	bybitP2PRequestAmount   = "2000" // Fixed RUB amount for querying suitable offers
	bybitP2PFixedTokenID    = "USDT"
	bybitP2PFixedCurrencyID = "RUB"
	bybitP2PTTL             = 5 * time.Minute
	bybitP2PClientTimeout   = 10 * time.Second // Bybit might be slower
)

// AllCurrenciesResponse maps currency codes (e.g., "usd") to their full names (e.g., "US Dollar").
type AllCurrenciesResponse map[string]string

// RatesAPIResponse represents the structure for caching fetched rates.
// It's not directly the JSON structure but what we store after processing.
type RatesAPIResponse struct {
	Date  string             `json:"date"`  // Date of the rates
	Rates map[string]float64 `json:"rates"` // Map of target currency (lowercase) to rate against base
}

// BybitP2PItem represents a single offer item from the Bybit P2P API.
type BybitP2PItem struct {
	ID           string   `json:"id"`
	NickName     string   `json:"nickName"`
	Price        string   `json:"price"`        // Price in currencyId (e.g., RUB) for 1 unit of tokenId (e.g., USDT)
	MinAmount    string   `json:"minAmount"`    // In currencyId (RUB)
	MaxAmount    string   `json:"maxAmount"`    // In currencyId (RUB)
	LastQuantity string   `json:"lastQuantity"` // Remaining quantity in tokenId (USDT)
	IsOnline     bool     `json:"isOnline"`
	Payments     []string `json:"payments"`
	CurrencyId   string   `json:"currencyId"` // e.g., "RUB"
	TokenId      string   `json:"tokenId"`    // e.g., "USDT"

	// Processed fields, not from JSON
	PriceFloat     float64
	MinAmountFloat float64
	MaxAmountFloat float64
}

// BybitP2PResult holds the list of items from the Bybit P2P API.
type BybitP2PResult struct {
	Count int            `json:"count"`
	Items []BybitP2PItem `json:"items"`
}

// BybitP2PResponse is the top-level structure for the Bybit P2P API response.
type BybitP2PResponse struct {
	RetCode int            `json:"ret_code"`
	RetMsg  string         `json:"ret_msg"`
	Result  BybitP2PResult `json:"result"`
	TimeNow string         `json:"time_now"`
}

type APICache struct {
	client          *http.Client
	bybitP2PClient  *http.Client // Separate client for Bybit with potentially different timeout
	ratesCache      *cache.Cache // Stores RatesAPIResponse, keyed by "rates_<lowercase_base_currency>"
	allCurrsCache   *cache.Cache // Stores AllCurrenciesResponse, keyed by "all_currencies"
	bybitCache      *cache.Cache // Stores *BybitP2PItem, keyed by "bybit_p2p_offer_USDT_RUB_side<0_or_1>"
	fetchLocks      sync.Map     // key: string (cacheKey), value: *sync.Mutex
	defaultRatesTTL time.Duration
	defaultAllTTL   time.Duration
}

func NewAPICache(ratesTTL, allCurrsTTL time.Duration) *APICache {
	return &APICache{
		client: &http.Client{
			Timeout: apiTimeout,
		},
		bybitP2PClient: &http.Client{
			Timeout: bybitP2PClientTimeout,
		},
		ratesCache:      cache.New(ratesTTL, ratesTTL*2),       // Default expiration, cleanup interval
		allCurrsCache:   cache.New(allCurrsTTL, allCurrsTTL*2), // Default expiration, cleanup interval
		bybitCache:      cache.New(bybitP2PTTL, bybitP2PTTL*2), // Cache for Bybit P2P offers
		defaultRatesTTL: ratesTTL,
		defaultAllTTL:   allCurrsTTL,
	}
}

func (ac *APICache) getLock(key string) *sync.Mutex {
	lock, _ := ac.fetchLocks.LoadOrStore(key, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func (ac *APICache) GetAllCurrencies(ctx context.Context) (AllCurrenciesResponse, error) {
	cacheKey := "all_currencies"
	if data, found := ac.allCurrsCache.Get(cacheKey); found {
		return data.(AllCurrenciesResponse), nil
	}

	lock := ac.getLock(cacheKey)
	lock.Lock()
	defer func() {
		lock.Unlock()
		ac.fetchLocks.Delete(cacheKey) // Clean up lock after fetch attempt
	}()

	// Double-check cache after acquiring lock
	if data, found := ac.allCurrsCache.Get(cacheKey); found {
		return data.(AllCurrenciesResponse), nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", allCurrenciesURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request for all currencies: %w", err)
	}

	resp, err := ac.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching all currencies: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("all currencies API returned status %d (%s)", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var currencies AllCurrenciesResponse
	if err := json.NewDecoder(resp.Body).Decode(&currencies); err != nil {
		return nil, fmt.Errorf("decoding all currencies response: %w", err)
	}

	ac.allCurrsCache.Set(cacheKey, currencies, ac.defaultAllTTL)
	return currencies, nil
}

// GetRates fetches exchange rates for a given base currency (e.g., "usd").
// It returns the processed rates data (date and map of target currency to rate).
func (ac *APICache) GetRates(ctx context.Context, baseCurrency string) (*RatesAPIResponse, error) {
	baseCurrency = strings.ToLower(baseCurrency) // API uses lowercase base currency in URL and keys
	cacheKey := "rates_" + baseCurrency

	if data, found := ac.ratesCache.Get(cacheKey); found {
		return data.(*RatesAPIResponse), nil
	}

	lock := ac.getLock(cacheKey)
	lock.Lock()
	defer func() {
		lock.Unlock()
		ac.fetchLocks.Delete(cacheKey) // Clean up lock
	}()

	if data, found := ac.ratesCache.Get(cacheKey); found {
		return data.(*RatesAPIResponse), nil
	}

	url := fmt.Sprintf(ratesBaseURL, baseCurrency)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request for rates of %s: %w", baseCurrency, err)
	}

	resp, err := ac.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching rates for %s from %s: %w", baseCurrency, url, err)
	}
	defer resp.Body.Close()

	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		// Error reading response body is serious, but we'll let the JSON decoder try if bodyBytes has content
	}
	resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes)) // Re-wrap for decoder

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rates API for %s (%s) returned status %d (%s)", baseCurrency, url, resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var tempResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&tempResp); err != nil {
		// If decoding fails, include part of the body for context if possible
		bodyStr := string(bodyBytes)
		if len(bodyStr) > 200 { // Limit length of body in error
			bodyStr = bodyStr[:200] + "..."
		}
		return nil, fmt.Errorf("decoding rates response for %s from %s (body: '%s'): %w", baseCurrency, url, bodyStr, err)
	}

	date, ok := tempResp["date"].(string)
	if !ok {
		return nil, fmt.Errorf("date field missing or not a string in rates response for %s from %s", baseCurrency, url)
	}

	var ratesMapInterface map[string]interface{}
	if val, found := tempResp[baseCurrency]; found {
		ratesMapInterface, ok = val.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("rates data for base '%s' is not a map in response from %s", baseCurrency, url)
		}
	} else {
		foundKey := false
		for apiKey, apiValue := range tempResp {
			if strings.ToLower(apiKey) == baseCurrency && apiKey != "date" {
				if actualMap, okInner := apiValue.(map[string]interface{}); okInner {
					ratesMapInterface = actualMap
					foundKey = true
					log.Printf("Found rates for '%s' under API key '%s' (case mismatch) from %s", baseCurrency, apiKey, url)
					break
				}
			}
		}
		if !foundKey {
			return nil, fmt.Errorf("base currency key '%s' not found in rates response map from %s", baseCurrency, url)
		}
	}

	finalRates := make(map[string]float64)
	for targetCurrencyAPI, rateInterface := range ratesMapInterface {
		targetCurrency := strings.ToLower(targetCurrencyAPI)
		if rate, ok := rateInterface.(float64); ok {
			finalRates[targetCurrency] = rate
		} else {
			if rateStr, okStr := rateInterface.(string); okStr {
				if parsedRate, errParse := strconv.ParseFloat(rateStr, 64); errParse == nil {
					finalRates[targetCurrency] = parsedRate
				} else {
					log.Printf("Warning: rate for target %s (from base %s) is not a float64 and not a parsable string: %T, %v. Parse error: %v. URL: %s", targetCurrency, baseCurrency, rateInterface, rateInterface, errParse, url)
				}
			} else {
				log.Printf("Warning: rate for target %s (from base %s) is not a float64 or string: %T, %v. URL: %s", targetCurrency, baseCurrency, rateInterface, rateInterface, url)
			}
		}
	}

	ratesAPIResponse := &RatesAPIResponse{
		Date:  date,
		Rates: finalRates,
	}

	ac.ratesCache.Set(cacheKey, ratesAPIResponse, ac.defaultRatesTTL)
	return ratesAPIResponse, nil
}

// GetConversionRate returns the rate for 1 unit of fromCurrency to toCurrency, and the date of the rate.
func (ac *APICache) GetConversionRate(ctx context.Context, fromCurrency, toCurrency string) (float64, string, error) {
	lcFromCurrency := strings.ToLower(fromCurrency)
	lcToCurrency := strings.ToLower(toCurrency)

	if lcFromCurrency == lcToCurrency {
		return 1.0, time.Now().Format("2006-01-02"), nil // Rate is 1 for same currency
	}

	ratesResp, err := ac.GetRates(ctx, lcFromCurrency)
	if err != nil {
		return 0, "", fmt.Errorf("getting rates for base %s when converting to %s: %w", lcFromCurrency, lcToCurrency, err)
	}

	rate, ok := ratesResp.Rates[lcToCurrency]
	if !ok {
		// Provide more context if a rate is not found
		availableTargets := make([]string, 0, len(ratesResp.Rates))
		for k := range ratesResp.Rates {
			availableTargets = append(availableTargets, k)
		}
		sort.Strings(availableTargets) // Sort for consistent logging
		log.Printf("Target currency '%s' not found in rates for base '%s'. Date: %s. Available targets (%d): %v", lcToCurrency, lcFromCurrency, ratesResp.Date, len(ratesResp.Rates), availableTargets)
		return 0, "", fmt.Errorf("conversion rate from %s to %s not found directly. Check API for '%s' base", lcFromCurrency, lcToCurrency, lcFromCurrency)
	}

	return rate, ratesResp.Date, nil
}

// GetBybitP2PBestOffer fetches the best P2P offer from Bybit for USDT/RUB.
// side: "0" for SELL USDT (user sells USDT, gets RUB), "1" for BUY USDT (user buys USDT, pays RUB).
// This function expects tokenId to be "USDT" and currencyId to be "RUB" as per current requirements.
func (ac *APICache) GetBybitP2PBestOffer(ctx context.Context, side string) (*BybitP2PItem, error) {
	tokenId := bybitP2PFixedTokenID
	currencyId := bybitP2PFixedCurrencyID
	cacheKey := fmt.Sprintf("bybit_p2p_offer_%s_%s_side%s", tokenId, currencyId, side)

	if data, found := ac.bybitCache.Get(cacheKey); found {
		if item, ok := data.(*BybitP2PItem); ok {
			return item, nil
		}
		log.Printf("Warning: Found data in Bybit cache for key %s, but it's not *BybitP2PItem", cacheKey)
	}

	lock := ac.getLock(cacheKey)
	lock.Lock()
	defer func() {
		lock.Unlock()
		ac.fetchLocks.Delete(cacheKey)
	}()

	if data, found := ac.bybitCache.Get(cacheKey); found {
		if item, ok := data.(*BybitP2PItem); ok {
			return item, nil
		}
	}

	payloadMap := map[string]interface{}{
		"userId":             "",
		"tokenId":            tokenId,
		"currencyId":         currencyId,
		"payment":            []string{"626", "382", "27", "383", "657"}, // Fixed as per instruction
		"side":               side,
		"size":               "10", // Fetch a few offers
		"page":               "1",
		"amount":             bybitP2PRequestAmount, // Fixed amount to filter relevant offers
		"vaMaker":            false,
		"bulkMaker":          false,
		"canTrade":           true,
		"verificationFilter": 2,
		"sortType":           "TRADE_PRICE", // Bybit sorts: side "1" (buy) -> price asc; side "0" (sell) -> price desc
		"paymentPeriod":      []interface{}{},
		"itemRegion":         1,
	}
	payloadBytes, err := json.Marshal(payloadMap)
	if err != nil {
		return nil, fmt.Errorf("marshaling Bybit P2P request payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", bybitP2PAPIURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("creating Bybit P2P request: %w", err)
	}

	// Set headers as per cURL example
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:139.0) Gecko/20100101 Firefox/139.0")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://www.bybit.com/")
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Origin", "https://www.bybit.com")
	req.Header.Set("Alt-Used", "api2.bybit.com")

	resp, err := ac.bybitP2PClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing Bybit P2P request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bybit P2P API returned status %d (%s)", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var bybitResp BybitP2PResponse
	if err := json.NewDecoder(resp.Body).Decode(&bybitResp); err != nil {
		return nil, fmt.Errorf("decoding Bybit P2P response: %w", err)
	}

	if bybitResp.RetCode != 0 {
		return nil, fmt.Errorf("bybit P2P API error: %s (code %d)", bybitResp.RetMsg, bybitResp.RetCode)
	}

	fixedAmountFloat, _ := strconv.ParseFloat(bybitP2PRequestAmount, 64)

	for i := range bybitResp.Result.Items {
		item := &bybitResp.Result.Items[i] // Get pointer to modify

		if !item.IsOnline || item.TokenId != tokenId || item.CurrencyId != currencyId {
			continue
		}

		priceF, err1 := strconv.ParseFloat(item.Price, 64)
		minAmountF, err2 := strconv.ParseFloat(item.MinAmount, 64)
		maxAmountF, err3 := strconv.ParseFloat(item.MaxAmount, 64)

		if err1 != nil || err2 != nil || err3 != nil {
			log.Printf("Warning: Could not parse numeric fields for Bybit offer ID %s: %v, %v, %v", item.ID, err1, err2, err3)
			continue
		}
		item.PriceFloat = priceF
		item.MinAmountFloat = minAmountF
		item.MaxAmountFloat = maxAmountF

		// Check if the fixed request amount (2000 RUB) is within the offer's limits
		if fixedAmountFloat >= minAmountF && fixedAmountFloat <= maxAmountF && priceF > 0 {
			// Due to "sortType":"TRADE_PRICE", the first valid item is the best.
			ac.bybitCache.Set(cacheKey, item, bybitP2PTTL)
			return item, nil
		}
	}

	return nil, fmt.Errorf("no suitable Bybit P2P offer found for side %s, token %s, currency %s with amount %s", side, tokenId, currencyId, bybitP2PRequestAmount)
}

// SortBybitP2PItems sorts items based on price.
// For side "1" (BUY USDT), ascending price is better.
// For side "0" (SELL USDT), descending price is better.
// This function is DEPRECATED if Bybit's sortType:"TRADE_PRICE" is reliable.
func SortBybitP2PItems(items []BybitP2PItem, side string) {
	sort.SliceStable(items, func(i, j int) bool {
		if side == "1" { // BUY USDT, want lower price
			return items[i].PriceFloat < items[j].PriceFloat
		}
		// SELL USDT, want higher price
		return items[i].PriceFloat > items[j].PriceFloat
	})
}
