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

// IMPORTANT WARNING: This code uses Mastercard's unofficial public API endpoint.
// This API is NOT officially documented or supported for automated/programmatic access.
// Risks:
// - Mastercard may detect automated requests and block the User-Agent or IP
// - The API endpoint URL or response format may change without notice
// - Rate limiting or CAPTCHA challenges may be introduced
// - Service may become unavailable or require authentication
// Consider these risks when deploying to production. For production use, consider:
// - Using official currency exchange APIs (though Mastercard doesn't offer one publicly)
// - Implementing fallback mechanisms to other exchange rate providers
// - Monitoring for API changes and failures

// fetchMastercardRates fetches fiat rates from the Mastercard API.
func (ac *APICache) fetchMastercardRates() error {
	if !mastercardCircuit.CanAttempt() {
		return fmt.Errorf("mastercard circuit breaker is open")
	}

	log.Println("Fetching Mastercard rates...")
	// Fixed timeout instead of scaling with currency count
	ctx, cancel := context.WithTimeout(context.Background(), mastercardTimeout*3)
	defer cancel()

	fetchedRates := make(map[string]float64)
	var mu sync.Mutex

	// Limit concurrent requests
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	var anySuccess bool

	// Fetch only priority currencies
	for _, fiat := range priorityFiats {
		if fiat == "USD" || fiat == "RUB" {
			continue
		}

		// Check context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
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
			anySuccess = true
			mu.Unlock()
		}(fiat)
	}

	wg.Wait()

	if !anySuccess {
		mastercardCircuit.RecordFailure()
		return fmt.Errorf("failed to fetch any Mastercard rates")
	}

	mastercardCircuit.RecordSuccess()

	// Only update if rates have changed
	hasChanges := false
	for key, rate := range fetchedRates {
		if oldRate, ok := ac.lastMastercardRates[key]; !ok || !floatEquals(oldRate, rate) {
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

// fetchMastercardRate fetches a single fiat rate pair from Mastercard.
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

	// Add minimal required headers for API access
	req.Header.Set("User-Agent", "AnswerFlow/1.0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Referer", "https://www.mastercard.com/global/en/personal/get-support/currency-exchange-rate-converter.html")

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
	if err != nil || !isValidFloat(rate) {
		return 0, fmt.Errorf("invalid rate: %s", result.Data.ConversionRate)
	}

	return rate, nil
}
