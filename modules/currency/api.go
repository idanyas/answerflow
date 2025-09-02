package currency

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Default provider constants
	allCurrenciesURL    = "https://cdn.jsdelivr.net/npm/@fawazahmed0/currency-api@latest/v1/currencies.json"
	ratesBaseURL        = "https://cdn.jsdelivr.net/npm/@fawazahmed0/currency-api@latest/v1/currencies/%s.json" // %s is lowercase base currency
	defaultAPITimeout   = 10 * time.Second
	backgroundUpdateTTL = 30 * time.Minute

	// Whitebird provider constants
	whitebirdAPIURL     = "https://admin-service.whitebird.io/api/v1/exchange/calculation"
	whitebirdAPITimeout = 15 * time.Second
)

// AllCurrenciesResponse maps currency codes (e.g., "usd") to their full names (e.g., "US Dollar").
type AllCurrenciesResponse map[string]string

// RatesAPIResponse represents the structure for caching fetched rates from the default provider.
type RatesAPIResponse struct {
	Date  string             `json:"date"`
	Rates map[string]float64 `json:"rates"`
}

// --- Whitebird API specific structs ---
type whitebirdRequestPayload struct {
	PromoCode    string                `json:"promoCode"`
	CurrencyPair whitebirdCurrencyPair `json:"currencyPair"`
	Calculation  whitebirdCalculation  `json:"calculation"`
	PaymentInfo  whitebirdPaymentInfo  `json:"paymentInfo"`
}

type whitebirdCurrencyPair struct {
	FromCurrency string `json:"fromCurrency"`
	ToCurrency   string `json:"toCurrency"`
}

type whitebirdCalculation struct {
	InputAsset float64 `json:"inputAsset"`
}

type whitebirdPaymentInfo struct {
	PaymentToken string `json:"paymentToken"`
}

type whitebirdResponse struct {
	Rate struct {
		PlainRatio string `json:"plainRatio"`
	} `json:"rate"`
}

// APICache holds all currency and rate data, updated by background processes.
type APICache struct {
	client *http.Client
	mu     sync.RWMutex // Protects all maps within this struct

	// Cache for default provider
	allCurrencies AllCurrenciesResponse
	defaultRates  map[string]*RatesAPIResponse // Key: lowercase base currency

	// Cache for Whitebird provider
	whitebirdRates map[string]float64 // Key: e.g., "RUB_USDT"
}

// NewAPICache initializes a new cache. Data is initially empty.
func NewAPICache() *APICache {
	return &APICache{
		client:         &http.Client{Timeout: defaultAPITimeout},
		allCurrencies:  make(AllCurrenciesResponse),
		defaultRates:   make(map[string]*RatesAPIResponse),
		whitebirdRates: make(map[string]float64),
	}
}

// StartBackgroundUpdaters launches goroutines that will periodically refresh the cache.
func (ac *APICache) StartBackgroundUpdaters() {
	log.Println("Starting background currency updaters...")
	go ac.updateDefaultProviderLoop()
	go ac.updateWhitebirdRatesLoop()
}

// --- Background Update Loops ---

func (ac *APICache) updateDefaultProviderLoop() {
	ticker := time.NewTicker(backgroundUpdateTTL)
	defer ticker.Stop()

	// Initial fetch on startup
	if err := ac.fetchDefaultProviderData(); err != nil {
		log.Printf("ERROR: Initial fetch for default provider failed: %v", err)
	}

	for range ticker.C {
		if err := ac.fetchDefaultProviderData(); err != nil {
			log.Printf("ERROR: Background update for default provider failed: %v", err)
		}
	}
}

func (ac *APICache) updateWhitebirdRatesLoop() {
	ticker := time.NewTicker(backgroundUpdateTTL)
	defer ticker.Stop()

	// Initial fetch on startup
	if err := ac.fetchWhitebirdRates(); err != nil {
		log.Printf("ERROR: Initial fetch for Whitebird provider failed: %v", err)
	}

	for range ticker.C {
		if err := ac.fetchWhitebirdRates(); err != nil {
			log.Printf("ERROR: Background update for Whitebird provider failed: %v", err)
		}
	}
}

// --- Data Fetching Logic ---

func (ac *APICache) fetchDefaultProviderData() error {
	log.Println("Fetching default provider data...")
	ctx, cancel := context.WithTimeout(context.Background(), defaultAPITimeout*2)
	defer cancel()

	// 1. Fetch all currencies list
	req, err := http.NewRequestWithContext(ctx, "GET", allCurrenciesURL, nil)
	if err != nil {
		return fmt.Errorf("creating request for all currencies: %w", err)
	}
	resp, err := ac.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetching all currencies: %w", err)
	}

	var fetchedCurrencies AllCurrenciesResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&fetchedCurrencies); err != nil {
			resp.Body.Close()
			return fmt.Errorf("decoding all currencies response: %w", err)
		}
	} else {
		status := resp.Status
		resp.Body.Close()
		return fmt.Errorf("all currencies API returned non-200 status: %s", status)
	}
	resp.Body.Close()

	// 2. Fetch rates for a set of important base currencies
	// This ensures we have common conversions readily available.
	baseCurrenciesToFetch := []string{"usd", "eur", "rub", "kzt", "byn", "usdt"} // ADDED "usdt"
	fetchedRates := make(map[string]*RatesAPIResponse)

	for _, base := range baseCurrenciesToFetch {
		ratesResp, err := ac.fetchRatesForBase(ctx, base)
		if err != nil {
			log.Printf("Warning: Failed to fetch rates for base '%s': %v. Skipping.", base, err)
			continue
		}
		fetchedRates[base] = ratesResp
		time.Sleep(200 * time.Millisecond) // Be nice to the API
	}

	// 3. Lock and update the cache
	ac.mu.Lock()
	ac.allCurrencies = fetchedCurrencies
	for base, rates := range fetchedRates {
		ac.defaultRates[base] = rates
	}
	ac.mu.Unlock()

	log.Println("Default provider data updated successfully.")
	return nil
}

func (ac *APICache) fetchRatesForBase(ctx context.Context, baseCurrency string) (*RatesAPIResponse, error) {
	url := fmt.Sprintf(ratesBaseURL, baseCurrency)
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
		return nil, fmt.Errorf("API returned status %s", resp.Status)
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	var tempResp map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &tempResp); err != nil {
		return nil, fmt.Errorf("decoding rates response: %w", err)
	}

	date, _ := tempResp["date"].(string)
	ratesMapInterface, ok := tempResp[baseCurrency].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("base currency key '%s' not found in rates response", baseCurrency)
	}

	finalRates := make(map[string]float64)
	for target, rateInterface := range ratesMapInterface {
		if rate, ok := rateInterface.(float64); ok {
			finalRates[strings.ToLower(target)] = rate
		}
	}

	return &RatesAPIResponse{Date: date, Rates: finalRates}, nil
}

func (ac *APICache) fetchWhitebirdRates() error {
	log.Println("Fetching Whitebird provider rates...")
	ctx, cancel := context.WithTimeout(context.Background(), whitebirdAPITimeout)
	defer cancel()

	pairs := []struct{ from, to, fromAPI, toAPI string }{
		{"RUB", "USDT", "RUB", "USDT_TON"},
		{"USDT", "RUB", "USDT_TON", "RUB"},
		{"BYN", "USDT", "BYN", "USDT_TON"},
	}

	fetchedRates := make(map[string]float64)

	for _, pair := range pairs {
		rate, err := ac.fetchSingleWhitebirdRate(ctx, pair.fromAPI, pair.toAPI)
		if err != nil {
			// Log the error but continue; a partial update is better than none.
			log.Printf("Warning: Failed to fetch Whitebird rate for %s->%s: %v", pair.from, pair.to, err)
			continue
		}
		cacheKey := fmt.Sprintf("%s_%s", pair.from, pair.to)
		fetchedRates[cacheKey] = rate
		time.Sleep(200 * time.Millisecond) // Be nice to the API
	}

	if len(fetchedRates) == 0 {
		return fmt.Errorf("all Whitebird rate fetches failed")
	}

	// Lock and update the cache
	ac.mu.Lock()
	for key, rate := range fetchedRates {
		ac.whitebirdRates[key] = rate
	}
	ac.mu.Unlock()

	log.Println("Whitebird provider rates updated successfully.")
	return nil
}

func (ac *APICache) fetchSingleWhitebirdRate(ctx context.Context, from, to string) (float64, error) {
	payload := whitebirdRequestPayload{
		PromoCode: "",
		CurrencyPair: whitebirdCurrencyPair{
			FromCurrency: from,
			ToCurrency:   to,
		},
		Calculation: whitebirdCalculation{InputAsset: 1},
		PaymentInfo: whitebirdPaymentInfo{PaymentToken: ""},
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshaling Whitebird payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", whitebirdAPIURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return 0, fmt.Errorf("creating Whitebird request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:142.0) Gecko/20100101 Firefox/142.0")
	req.Header.Set("Content-Type", "application/json")

	resp, err := ac.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("executing Whitebird request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("API returned non-200 status: %s", resp.Status)
	}

	var wbResp whitebirdResponse
	if err := json.NewDecoder(resp.Body).Decode(&wbResp); err != nil {
		return 0, fmt.Errorf("decoding Whitebird response: %w", err)
	}

	rate, err := strconv.ParseFloat(wbResp.Rate.PlainRatio, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing Whitebird rate '%s': %w", wbResp.Rate.PlainRatio, err)
	}

	return rate, nil
}

// --- Public Cache Accessors ---

// GetAllCurrencies retrieves the map of all known currencies from the cache.
func (ac *APICache) GetAllCurrencies() (AllCurrenciesResponse, error) {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	if len(ac.allCurrencies) == 0 {
		return nil, fmt.Errorf("currency list not yet available in cache")
	}
	// Return a copy to prevent race conditions on the caller's side
	dataCopy := make(AllCurrenciesResponse, len(ac.allCurrencies))
	for k, v := range ac.allCurrencies {
		dataCopy[k] = v
	}
	return dataCopy, nil
}

// GetConversionRate returns the rate for 1 unit of fromCurrency to toCurrency from the default provider.
func (ac *APICache) GetConversionRate(fromCurrency, toCurrency string) (float64, string, error) {
	lcFrom := strings.ToLower(fromCurrency)
	lcTo := strings.ToLower(toCurrency)

	if lcFrom == lcTo {
		return 1.0, time.Now().Format("2006-01-02"), nil
	}

	ac.mu.RLock()
	defer ac.mu.RUnlock()

	ratesResp, ok := ac.defaultRates[lcFrom]
	if !ok {
		return 0, "", fmt.Errorf("base currency '%s' not available in cache", fromCurrency)
	}

	rate, ok := ratesResp.Rates[lcTo]
	if !ok {
		return 0, "", fmt.Errorf("target currency '%s' not found for base '%s'", toCurrency, fromCurrency)
	}

	return rate, ratesResp.Date, nil
}

// GetWhitebirdRate retrieves a pre-fetched raw rate from the Whitebird provider.
func (ac *APICache) GetWhitebirdRate(fromCurrency, toCurrency string) (float64, error) {
	cacheKey := fmt.Sprintf("%s_%s", fromCurrency, toCurrency)
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	rate, ok := ac.whitebirdRates[cacheKey]
	if !ok {
		return 0, fmt.Errorf("whitebird rate for %s to %s not available in cache", fromCurrency, toCurrency)
	}
	return rate, nil
}

// DEPRECATED: GetRates is kept for compatibility in case any part of the code still uses it,
// but GetConversionRate is the preferred method for external use.
func (ac *APICache) GetRates(ctx context.Context, baseCurrency string) (*RatesAPIResponse, error) {
	baseCurrency = strings.ToLower(baseCurrency)
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	ratesResp, ok := ac.defaultRates[baseCurrency]
	if !ok {
		return nil, fmt.Errorf("rates for base '%s' not available in cache", baseCurrency)
	}
	return ratesResp, nil
}
