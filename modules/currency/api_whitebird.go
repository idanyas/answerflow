package currency

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

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
		PlainRatio string `json:"plainRatio"`
		Ratio      string `json:"ratio"`
	} `json:"rate"`
	Calculation struct {
		OutputAsset string `json:"outputAsset"`
	} `json:"calculation"`
}

func (ac *APICache) fetchWhitebirdRates() error {
	if !whitebirdCircuit.CanAttempt() {
		return fmt.Errorf("circuit breaker open")
	}

	log.Println("Fetching Whitebird rates...")
	ctx, cancel := context.WithTimeout(context.Background(), whitebirdAPITimeout*2)
	defer cancel()

	pairs := []struct{ from, to string }{
		{"RUB", "TON"},
		{"TON", "RUB"},
	}

	fetchedRates := make(map[string]float64)
	const amount = 10000.0

	for _, pair := range pairs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		rate, err := ac.fetchSingleWhitebirdRate(ctx, pair.from, pair.to, amount)
		if err != nil {
			log.Printf("Warning: Whitebird %s->%s failed: %v", pair.from, pair.to, err)
			whitebirdCircuit.RecordFailure()
			continue
		}

		var key string
		if pair.from == "RUB" && pair.to == "TON" {
			key = "RUB_TON_BUY"
		} else if pair.from == "TON" && pair.to == "RUB" {
			key = "TON_RUB_SELL"
		}
		fetchedRates[key] = rate

		log.Printf("Whitebird %s: rate=%f", key, rate)
		time.Sleep(200 * time.Millisecond)
	}

	if len(fetchedRates) == 0 {
		whitebirdCircuit.RecordFailure()
		return fmt.Errorf("all fetches failed")
	}

	whitebirdCircuit.RecordSuccess()

	hasChanges := false
	for key, rate := range fetchedRates {
		if oldRate, ok := ac.lastWhitebirdRates[key]; !ok || !floatEquals(oldRate, rate) {
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

		if !ac.ValidateWhitebirdRates() {
			log.Printf("Warning: Whitebird rates validation failed")
		} else {
			log.Printf("Whitebird rates updated: %d rates", len(fetchedRates))
		}
	}

	return nil
}

func (ac *APICache) fetchSingleWhitebirdRate(ctx context.Context, from, to string, amount float64) (float64, error) {
	if err := whitebirdLimiter.Wait(ctx); err != nil {
		return 0, err
	}

	payload := whitebirdRequestPayload{
		CurrencyPair: whitebirdCurrencyPair{from, to},
		Calculation:  whitebirdCalculation{amount},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", whitebirdAPIURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return 0, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://whitebird.io")

	resp, err := ac.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %s", resp.Status)
	}

	var wbResp whitebirdResponse
	if err := json.NewDecoder(resp.Body).Decode(&wbResp); err != nil {
		return 0, err
	}

	outputAmount, err := strconv.ParseFloat(wbResp.Calculation.OutputAsset, 64)
	if err != nil || !isValidFloat(outputAmount) {
		return 0, fmt.Errorf("invalid output")
	}

	var effectiveRate float64
	if from == "RUB" && to == "TON" {
		effectiveRate = amount / outputAmount
	} else if from == "TON" && to == "RUB" {
		effectiveRate = outputAmount / amount
	} else {
		return 0, fmt.Errorf("unsupported pair")
	}

	if effectiveRate < whitebirdRateMin || effectiveRate > whitebirdRateMax {
		return 0, fmt.Errorf("rate outside valid range")
	}

	return effectiveRate, nil
}
