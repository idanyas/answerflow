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

// whitebirdRequestPayload defines the JSON structure for a Whitebird API request.
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

// whitebirdResponse defines the JSON structure of a Whitebird API response.
type whitebirdResponse struct {
	Rate struct {
		PlainRatio string `json:"plainRatio"` // Rate without fees
		Ratio      string `json:"ratio"`      // Rate with fees included - THIS IS WHAT WE USE
	} `json:"rate"`
	Calculation struct {
		OutputAsset string `json:"outputAsset"` // Amount received after fees
	} `json:"calculation"`
}

// fetchWhitebirdRates fetches RUB <-> TON rates from the Whitebird API.
func (ac *APICache) fetchWhitebirdRates() error {
	if !whitebirdCircuit.CanAttempt() {
		return fmt.Errorf("whitebird circuit breaker is open")
	}

	log.Println("Fetching Whitebird rates...")
	// Ensure timeout doesn't exceed parent context
	timeout := whitebirdAPITimeout
	if timeout > requestTimeout {
		timeout = requestTimeout - 500*time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout*2)
	defer cancel()

	pairs := []struct{ from, to string }{
		{"RUB", "TON"},
		{"TON", "RUB"},
	}

	fetchedRates := make(map[string]float64)

	// Use a single representative amount (10,000) for rate determination
	const representativeAmount = 10000.0

	for _, pair := range pairs {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		rate, err := ac.fetchSingleWhitebirdRate(ctx, pair.from, pair.to, representativeAmount)
		if err != nil {
			log.Printf("Warning: Failed to fetch Whitebird %s->%s: %v", pair.from, pair.to, err)
			whitebirdCircuit.RecordFailure()
			continue
		}

		// Store with direction-specific keys
		var cacheKey string
		if pair.from == "RUB" && pair.to == "TON" {
			cacheKey = "RUB_TON_BUY" // Buying TON with RUB
		} else if pair.from == "TON" && pair.to == "RUB" {
			cacheKey = "TON_RUB_SELL" // Selling TON for RUB
		}
		fetchedRates[cacheKey] = rate

		// Log the fetched rate for debugging
		log.Printf("Whitebird %s: rate=%f (effective rate with fees, amount=%.0f)", cacheKey, rate, representativeAmount)

		time.Sleep(200 * time.Millisecond)
	}

	if len(fetchedRates) == 0 {
		whitebirdCircuit.RecordFailure()
		return fmt.Errorf("all Whitebird rate fetches failed")
	}

	whitebirdCircuit.RecordSuccess()

	// Only update if rates have actually changed
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

		// Validate rates after update
		if !ac.ValidateWhitebirdRates() {
			log.Printf("Warning: Whitebird rates validation failed - rates may be incorrect")
		} else {
			log.Printf("Whitebird rates updated successfully: %d rates", len(fetchedRates))
		}
	}

	return nil
}

// fetchSingleWhitebirdRate fetches a single rate pair from Whitebird with specific amount.
func (ac *APICache) fetchSingleWhitebirdRate(ctx context.Context, from, to string, testAmount float64) (float64, error) {
	// Apply rate limiting
	if err := whitebirdLimiter.Wait(ctx); err != nil {
		return 0, fmt.Errorf("rate limit error: %w", err)
	}

	payload := whitebirdRequestPayload{
		CurrencyPair: whitebirdCurrencyPair{
			FromCurrency: from,
			ToCurrency:   to,
		},
		Calculation: whitebirdCalculation{InputAsset: testAmount},
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

	// Parse the output amount to calculate the effective rate
	outputAmount, err := strconv.ParseFloat(wbResp.Calculation.OutputAsset, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing output amount '%s': %w", wbResp.Calculation.OutputAsset, err)
	}

	// Validate output amount
	if !isValidFloat(outputAmount) {
		return 0, fmt.Errorf("invalid output amount: %f", outputAmount)
	}

	// Calculate effective rate (includes fees)
	var effectiveRate float64

	if from == "RUB" && to == "TON" {
		// RUB -> TON: Rate is RUB per TON (how many RUB to buy 1 TON)
		effectiveRate = testAmount / outputAmount
	} else if from == "TON" && to == "RUB" {
		// TON -> RUB: Rate is also RUB per TON (how many RUB you get per 1 TON)
		effectiveRate = outputAmount / testAmount
	} else {
		return 0, fmt.Errorf("unsupported currency pair: %s->%s", from, to)
	}

	// Validate the rate is within reasonable bounds
	if effectiveRate < whitebirdRateMin || effectiveRate > whitebirdRateMax {
		return 0, fmt.Errorf("rate %f outside valid range [%f, %f]", effectiveRate, whitebirdRateMin, whitebirdRateMax)
	}

	log.Printf("Whitebird %s->%s: input=%f, output=%f, effective_rate=%f RUB/TON",
		from, to, testAmount, outputAmount, effectiveRate)

	return effectiveRate, nil
}
