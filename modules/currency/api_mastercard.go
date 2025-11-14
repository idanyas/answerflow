package currency

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Priority currencies to fetch first (most commonly used)
var priorityFiatCurrencies = []string{
	"EUR", "GBP", "JPY", "CNY", "CHF", "CAD", "AUD", "KRW", "HKD", "SGD",
	"SEK", "NOK", "DKK", "INR", "MXN", "BRL", "ZAR", "TRY", "PLN", "THB",
}

var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0",
}

func getRandomUserAgent() string {
	return userAgents[rand.Intn(len(userAgents))]
}

type adaptiveFetcher struct {
	successCount   atomic.Int32
	failureCount   atomic.Int32
	currentWorkers atomic.Int32
	mu             sync.Mutex
}

func (af *adaptiveFetcher) recordSuccess() {
	af.successCount.Add(1)
	af.adjustConcurrency()
}

func (af *adaptiveFetcher) recordFailure() {
	af.failureCount.Add(1)
	af.adjustConcurrency()
}

func (af *adaptiveFetcher) adjustConcurrency() {
	af.mu.Lock()
	defer af.mu.Unlock()

	success := af.successCount.Load()
	failure := af.failureCount.Load()
	total := success + failure

	if total < 10 {
		return // Not enough data
	}

	successRate := float64(success) / float64(total)
	current := af.currentWorkers.Load()

	// Increase workers if success rate > 90% and current < 7
	if successRate > 0.9 && current < 7 {
		af.currentWorkers.Store(current + 1)
		log.Printf("Adaptive: Increasing workers to %d (success rate: %.1f%%)", current+1, successRate*100)
	}

	// Decrease workers if success rate < 70%
	if successRate < 0.7 && current > 1 {
		af.currentWorkers.Store(current - 1)
		log.Printf("Adaptive: Decreasing workers to %d (success rate: %.1f%%)", current-1, successRate*100)
	}
}

func (af *adaptiveFetcher) getWorkerCount() int {
	count := af.currentWorkers.Load()
	if count == 0 {
		count = 2 // Start with 2 workers
		af.currentWorkers.Store(count)
	}
	return int(count)
}

func (ac *APICache) fetchMastercardRates() error {
	if !mastercardCircuit.CanAttempt() {
		return fmt.Errorf("circuit breaker open")
	}

	log.Println("Fetching Mastercard rates with adaptive smart fetcher...")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	fetchedRates := make(map[string]float64)
	var mu sync.Mutex

	// Separate currencies into priority and regular
	prioritySet := make(map[string]bool)
	for _, curr := range priorityFiatCurrencies {
		prioritySet[curr] = true
	}

	var priorityCurrencies, regularCurrencies []string
	for _, fiat := range supportedFiats {
		if fiat == CurrencyUSD {
			continue
		}
		if prioritySet[fiat] {
			priorityCurrencies = append(priorityCurrencies, fiat)
		} else {
			regularCurrencies = append(regularCurrencies, fiat)
		}
	}

	log.Printf("Fetching %d priority currencies first, then %d regular currencies",
		len(priorityCurrencies), len(regularCurrencies))

	fetcher := &adaptiveFetcher{}
	fetcher.currentWorkers.Store(2) // Start with 2 workers

	// Fetch priority currencies first with lower concurrency
	ac.fetchCurrencyBatch(ctx, priorityCurrencies, fetchedRates, &mu, fetcher, 2)

	// Then fetch regular currencies with adaptive concurrency
	ac.fetchCurrencyBatch(ctx, regularCurrencies, fetchedRates, &mu, fetcher, 5)

	successCount := len(fetchedRates)
	failCount := len(supportedFiats) - 1 - successCount

	log.Printf("Mastercard fetch complete: %d successes, %d failures", successCount, failCount)

	if successCount == 0 {
		mastercardCircuit.RecordFailure()
		return fmt.Errorf("no rates fetched (all attempts failed)")
	}

	// Even partial success is acceptable - record success
	mastercardCircuit.RecordSuccess()

	ac.mu.Lock()
	for key, rate := range fetchedRates {
		ac.mastercardRates[key] = rate
		ac.lastMastercardRates[key] = rate
	}
	ac.mastercardLastUpdate = time.Now()
	ac.mu.Unlock()

	log.Printf("Mastercard rates updated: %d pairs", len(fetchedRates))

	if failCount > 0 {
		log.Printf("Warning: %d currencies failed to fetch, using cached values if available", failCount)
	}

	return nil
}

func (ac *APICache) fetchCurrencyBatch(ctx context.Context, currencies []string, fetchedRates map[string]float64,
	mu *sync.Mutex, fetcher *adaptiveFetcher, maxWorkers int32) {

	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup

	for i, fiat := range currencies {
		// Adaptive worker count
		currentMax := fetcher.getWorkerCount()
		if currentMax > int(maxWorkers) {
			currentMax = int(maxWorkers)
		}

		// Throttle to current worker count
		for len(sem) >= currentMax {
			time.Sleep(50 * time.Millisecond)
		}

		wg.Add(1)
		go func(targetFiat string, index int) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			// Add human-like jitter (100-300ms)
			jitter := time.Duration(100+rand.Intn(200)) * time.Millisecond
			time.Sleep(jitter)

			// Try to fetch with smart retry
			rate, err := ac.fetchMastercardRateWithRetry(ctx, CurrencyUSD, targetFiat, 3)
			if err != nil {
				fetcher.recordFailure()
				// Only log first few failures
				if fetcher.failureCount.Load() <= 5 {
					log.Printf("Failed to fetch USD->%s after retries: %v", targetFiat, err)
				}
				return
			}

			fetcher.recordSuccess()
			mu.Lock()
			fetchedRates[fmt.Sprintf("USD_%s", targetFiat)] = rate
			mu.Unlock()

			// Log progress every 20 currencies
			if (index+1)%20 == 0 {
				mu.Lock()
				currentCount := len(fetchedRates)
				mu.Unlock()
				log.Printf("Progress: %d/%d currencies fetched", currentCount, len(supportedFiats)-1)
			}
		}(fiat, i)
	}

	wg.Wait()
}

func (ac *APICache) fetchMastercardRateWithRetry(ctx context.Context, from, to string, maxRetries int) (float64, error) {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 3s, 9s
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}

			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return 0, ctx.Err()
			}

			log.Printf("Retry attempt %d/%d for USD->%s after %v", attempt+1, maxRetries, to, backoff)
		}

		rate, err := ac.fetchMastercardRate(ctx, from, to)
		if err == nil {
			return rate, nil
		}

		lastErr = err

		// Don't retry on context errors
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}

		// Only retry on 403 or timeout errors
		if !isMastercardRetryableError(err) {
			return 0, err
		}
	}

	return 0, fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

func isMastercardRetryableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Retry on 403 Forbidden, 429 Too Many Requests, or timeout
	return containsAny(errStr, "403", "429", "timeout", "deadline exceeded")
}

func containsAny(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if len(s) >= len(substr) {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
		}
	}
	return false
}

func (ac *APICache) fetchMastercardRate(ctx context.Context, from, to string) (float64, error) {
	if err := mastercardLimiter.Wait(ctx); err != nil {
		return 0, err
	}

	// Per-request timeout
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	select {
	case <-requestCtx.Done():
		return 0, requestCtx.Err()
	default:
	}

	url := fmt.Sprintf("%s?exchange_date=0000-00-00&transaction_currency=%s&cardholder_billing_currency=%s&bank_fee=0&transaction_amount=10000000",
		mastercardAPIURL, from, to)

	req, err := http.NewRequestWithContext(requestCtx, "GET", url, nil)
	if err != nil {
		return 0, err
	}

	// Use varied, realistic browser headers
	userAgent := getRandomUserAgent()
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Referer", "https://www.mastercard.com/global/en/personal/get-support/currency-exchange-rate-converter.html")
	req.Header.Set("Origin", "https://www.mastercard.com")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("DNT", "1")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")

	resp, err := ac.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %s", resp.Status)
	}

	// Handle gzip decompression manually since we explicitly set Accept-Encoding
	var reader io.ReadCloser
	switch resp.Header.Get("Content-Encoding") {
	case "gzip":
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return 0, fmt.Errorf("failed to create gzip reader: %w", err)
		}
		reader = gzipReader
		defer gzipReader.Close()
	default:
		reader = resp.Body
	}

	limitedReader := io.LimitReader(reader, maxHTTPResponseSize)

	var result struct {
		Data struct {
			ConversionRate string `json:"conversionRate"`
		} `json:"data"`
	}

	if err := json.NewDecoder(limitedReader).Decode(&result); err != nil {
		return 0, fmt.Errorf("failed to decode response: %w", err)
	}

	if result.Data.ConversionRate == "" {
		return 0, fmt.Errorf("empty conversion rate in response")
	}

	rate, err := strconv.ParseFloat(result.Data.ConversionRate, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid conversion rate '%s': %w", result.Data.ConversionRate, err)
	}

	if rate <= 0 || !isValidFloat(rate) {
		return 0, fmt.Errorf("invalid rate value: %f", rate)
	}

	return rate, nil
}
