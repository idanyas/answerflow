package currency

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

func (ac *APICache) fetchBybitRates() error {
	if !bybitCircuit.CanAttempt() {
		return fmt.Errorf("circuit breaker open")
	}

	log.Println("Fetching Bybit rates...")
	ctx, cancel := context.WithTimeout(context.Background(), bybitAPITimeout*3)
	defer cancel()

	// Fetch top 50 most popular pairs for immediate availability
	// Remaining symbols are loaded lazily via EnsureBybitSymbol
	// FIXED: Removed duplicate MATICUSDT, OPUSDT; replaced MATICUSDT with POLUSDT; removed invalid MKRUSDT
	keyPairs := []string{
		"TONUSDT", "BTCUSDT", "ETHUSDT", "SOLUSDT", "ADAUSDT", "DOGEUSDT",
		"XRPUSDT", "DOTUSDT", "LINKUSDT", "UNIUSDT", "ATOMUSDT", "AVAXUSDT",
		"NEARUSDT", "APTUSDT", "ARBUSDT", "OPUSDT", "POLUSDT", "LTCUSDT",
		"BCHUSDT", "ETCUSDT", "FILUSDT", "TRXUSDT", "XLMUSDT", "SHIBUSDT",
		"PEPEUSDT", "WIFUSDT", "BONKUSDT", "FLOKIUSDT", "INJUSDT", "SUIUSDT",
		"RENDERUSDT", "ICPUSDT", "AAVEUSDT", "LDOUSDT",
		"BNBUSDT", "ALGOUSDT", "SANDUSDT", "MANAUSDT", "AXSUSDT",
		"GALAUSDT", "ENJUSDT", "CHZUSDT", "FLOWUSDT", "GRTUSDT", "BATUSDT",
		"ZRXUSDT", "COMPUSDT",
	}

	fetchedRates := make(map[string]*BybitRate)
	var mu sync.Mutex
	var anySuccess bool
	var failCount int

	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup

loop:
	for _, symbol := range keyPairs {
		select {
		case <-ctx.Done():
			log.Printf("Bybit fetch context cancelled")
			if !anySuccess {
				return ctx.Err()
			}
			break loop
		default:
		}

		wg.Add(1)
		go func(sym string) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			rate, err := ac.fetchBybitOrderbook(ctx, sym)
			if err != nil {
				mu.Lock()
				failCount++
				mu.Unlock()
				log.Printf("Failed to fetch Bybit rate for %s: %v", sym, err)
				return
			}
			mu.Lock()
			fetchedRates[sym] = rate
			anySuccess = true
			mu.Unlock()
		}(symbol)
	}

	wg.Wait()

	log.Printf("Bybit fetch complete: %d successes, %d failures", len(fetchedRates), failCount)

	if !anySuccess {
		bybitCircuit.RecordFailure()
		return fmt.Errorf("no rates fetched (all %d attempts failed)", failCount)
	}

	bybitCircuit.RecordSuccess()

	ac.mu.Lock()
	for key, rate := range fetchedRates {
		ac.bybitRates[key] = rate
		ac.lastBybitRates[key] = rate
		ac.tradeablePairs[key] = true

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

	log.Printf("Bybit rates updated: %d pairs (remaining %d symbols available via lazy loading)",
		len(fetchedRates), len(supportedCryptos)-len(fetchedRates))

	// Save to file after successful fetch
	ac.SaveToFileAsync()

	return nil
}

func (ac *APICache) fetchBybitOrderbook(ctx context.Context, symbol string) (*BybitRate, error) {
	if err := bybitLimiter.Wait(ctx); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Use limit=200 for spot, as required by spec to get deeper liquidity and realistic pricing
	url := fmt.Sprintf("%s?category=spot&symbol=%s&limit=200", bybitOrderbookURL, symbol)
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

	// Limit response body size
	limitedReader := io.LimitReader(resp.Body, maxHTTPResponseSize)

	var result struct {
		RetCode int `json:"retCode"`
		Result  struct {
			A [][]string `json:"a"`
			B [][]string `json:"b"`
		} `json:"result"`
	}

	if err := json.NewDecoder(limitedReader).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if result.RetCode != 0 {
		return nil, fmt.Errorf("API returned error code: %d", result.RetCode)
	}

	if len(result.Result.A) == 0 || len(result.Result.B) == 0 {
		return nil, fmt.Errorf("empty order book")
	}

	// Build slice dynamically to avoid nil entries
	orderBookAsks := make([][]float64, 0, len(result.Result.A))
	for _, ask := range result.Result.A {
		if len(ask) >= 2 {
			price, errP := strconv.ParseFloat(ask[0], 64)
			size, errS := strconv.ParseFloat(ask[1], 64)
			if errP != nil || errS != nil {
				log.Printf("Warning: failed to parse Bybit ask [%v, %v] for %s", ask[0], ask[1], symbol)
				continue
			}
			if isValidFloat(price) && isValidFloat(size) {
				orderBookAsks = append(orderBookAsks, []float64{price, size})
			}
		}
	}

	orderBookBids := make([][]float64, 0, len(result.Result.B))
	for _, bid := range result.Result.B {
		if len(bid) >= 2 {
			price, errP := strconv.ParseFloat(bid[0], 64)
			size, errS := strconv.ParseFloat(bid[1], 64)
			if errP != nil || errS != nil {
				log.Printf("Warning: failed to parse Bybit bid [%v, %v] for %s", bid[0], bid[1], symbol)
				continue
			}
			if isValidFloat(price) && isValidFloat(size) {
				orderBookBids = append(orderBookBids, []float64{price, size})
			}
		}
	}

	if len(orderBookAsks) == 0 || len(orderBookBids) == 0 {
		return nil, fmt.Errorf("no valid order book levels")
	}

	return &BybitRate{
		BestBid:       orderBookBids[0][0],
		BestAsk:       orderBookAsks[0][0],
		OrderBookBids: orderBookBids,
		OrderBookAsks: orderBookAsks,
		LastUpdate:    time.Now(),
	}, nil
}

// EnsureBybitSymbol lazily fetches and caches a symbol's orderbook if it's not already known.
// This allows supporting a large list of symbols (515+) without pre-fetching all of them.
// Uses retry logic for resilience against transient network errors.
func (ac *APICache) EnsureBybitSymbol(symbol string) error {
	// Fast path: check with read lock first
	ac.mu.RLock()
	if _, ok := ac.bybitRates[symbol]; ok {
		ac.mu.RUnlock()
		return nil
	}
	// Check if already being fetched
	if _, fetching := ac.symbolsFetching[symbol]; fetching {
		ac.mu.RUnlock()
		// Wait briefly and retry
		time.Sleep(100 * time.Millisecond)
		return ac.EnsureBybitSymbol(symbol)
	}
	ac.mu.RUnlock()

	// Symbol not found, acquire write lock for fetching
	ac.mu.Lock()
	// Double-check inside write lock (another goroutine might have fetched it)
	if _, ok := ac.bybitRates[symbol]; ok {
		ac.mu.Unlock()
		return nil
	}
	// Check again if being fetched
	if _, fetching := ac.symbolsFetching[symbol]; fetching {
		ac.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		return ac.EnsureBybitSymbol(symbol)
	}

	// Check circuit breaker while holding lock
	if !bybitCircuit.CanAttempt() {
		ac.mu.Unlock()
		return fmt.Errorf("bybit circuit breaker open")
	}

	// Mark symbol as being fetched
	ac.symbolsFetching[symbol] = true
	ac.mu.Unlock()

	// Fetch without holding lock (use retry logic for resilience)
	var rate *BybitRate
	err := retryWithBackoff(context.Background(), func() error {
		ctx, cancel := context.WithTimeout(context.Background(), bybitAPITimeout*2)
		defer cancel()

		r, e := ac.fetchBybitOrderbook(ctx, symbol)
		if e != nil {
			return e
		}
		rate = r
		return nil
	})

	// Clean up fetching status and store result
	ac.mu.Lock()
	delete(ac.symbolsFetching, symbol)

	if err != nil {
		ac.mu.Unlock()
		bybitCircuit.RecordFailure()
		return fmt.Errorf("failed to fetch symbol %s: %w", symbol, err)
	}

	bybitCircuit.RecordSuccess()
	ac.bybitRates[symbol] = rate
	ac.lastBybitRates[symbol] = rate
	ac.tradeablePairs[symbol] = true
	ac.bybitLastUpdate = time.Now()
	ac.pairsLastCheck = time.Now()
	ac.mu.Unlock()

	log.Printf("Lazily loaded Bybit symbol: %s", symbol)

	// Save to file after lazy loading new symbol
	ac.SaveToFileAsync()

	return nil
}
