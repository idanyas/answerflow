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

func (ac *APICache) fetchMastercardRates() error {
	if !mastercardCircuit.CanAttempt() {
		return fmt.Errorf("circuit breaker open")
	}

	log.Println("Fetching Mastercard rates...")
	ctx, cancel := context.WithTimeout(context.Background(), mastercardTimeout*3)
	defer cancel()

	fetchedRates := make(map[string]float64)
	var mu sync.Mutex
	var anySuccess bool

	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup

	for _, fiat := range priorityFiats {
		if fiat == "USD" || fiat == "RUB" {
			continue
		}

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
		return fmt.Errorf("no rates fetched")
	}

	mastercardCircuit.RecordSuccess()

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

func (ac *APICache) fetchMastercardRate(ctx context.Context, from, to string) (float64, error) {
	if err := mastercardLimiter.Wait(ctx); err != nil {
		return 0, err
	}

	url := fmt.Sprintf("%s?exchange_date=0000-00-00&transaction_currency=%s&cardholder_billing_currency=%s&bank_fee=0&transaction_amount=10000000",
		mastercardAPIURL, from, to)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}

	req.Header.Set("User-Agent", "AnswerFlow/1.0")
	req.Header.Set("Accept", "*/*")
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
		return 0, fmt.Errorf("invalid rate")
	}

	return rate, nil
}
