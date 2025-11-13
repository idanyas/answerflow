package currency

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Helper function
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// fetchBybitRates fetches crypto rates from the Bybit API.
func (ac *APICache) fetchBybitRates() error {
	if !bybitCircuit.CanAttempt() {
		return fmt.Errorf("bybit circuit breaker is open")
	}

	log.Println("Fetching Bybit rates...")
	// Ensure timeout doesn't exceed parent context
	timeout := bybitAPITimeout
	if timeout > requestTimeout {
		timeout = requestTimeout - 500*time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout*3)
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
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	var anySuccess bool

	for _, symbol := range keyPairs {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

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
			anySuccess = true
			mu.Unlock()
		}(symbol)
	}

	wg.Wait()

	if !anySuccess {
		bybitCircuit.RecordFailure()
		return fmt.Errorf("failed to fetch any Bybit rates")
	}

	bybitCircuit.RecordSuccess()

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
				DecimalPlaces:      GetCurrencyDecimalPlaces(cryptoCode),
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

// fetchBybitOrderbook fetches the order book for a specific symbol from Bybit.
func (ac *APICache) fetchBybitOrderbook(ctx context.Context, symbol string) (*BybitRate, error) {
	// Apply rate limiting
	if err := bybitLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit error: %w", err)
	}

	// Check context before making request
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Use fixed reasonable depth for all pairs (100 levels)
	limit := 100

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
			if isValidFloat(price) && isValidFloat(size) {
				orderBookAsks = append(orderBookAsks, []float64{price, size})
			}
		}
	}

	orderBookBids := make([][]float64, 0, len(result.Result.B))
	for _, bid := range result.Result.B {
		if len(bid) >= 2 {
			price, _ := strconv.ParseFloat(bid[0], 64)
			size, _ := strconv.ParseFloat(bid[1], 64)
			if isValidFloat(price) && isValidFloat(size) {
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
		Volume24h:     0, // Not fetched to avoid double API call
	}, nil
}
