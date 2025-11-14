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

func (ac *APICache) fetchBybitRates() error {
	if !bybitCircuit.CanAttempt() {
		return fmt.Errorf("circuit breaker open")
	}

	log.Println("Fetching Bybit rates...")
	ctx, cancel := context.WithTimeout(context.Background(), bybitAPITimeout*3)
	defer cancel()

	keyPairs := []string{
		"TONUSDT", "BTCUSDT", "ETHUSDT", "SOLUSDT", "ADAUSDT", "DOGEUSDT",
		"XRPUSDT", "DOTUSDT", "LINKUSDT", "UNIUSDT", "ATOMUSDT", "AVAXUSDT",
		"NEARUSDT", "APTUSDT", "ARBUSDT", "OPUSDT",
	}

	fetchedRates := make(map[string]*BybitRate)
	var mu sync.Mutex
	var anySuccess bool

	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup

	for _, symbol := range keyPairs {
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
		return fmt.Errorf("no rates fetched")
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

	log.Printf("Bybit rates updated: %d pairs", len(fetchedRates))
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

	url := fmt.Sprintf("%s?category=spot&symbol=%s&limit=100", bybitOrderbookURL, symbol)
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
			A [][]string `json:"a"`
			B [][]string `json:"b"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if result.RetCode != 0 || len(result.Result.A) == 0 || len(result.Result.B) == 0 {
		return nil, fmt.Errorf("invalid response")
	}

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
	}, nil
}
