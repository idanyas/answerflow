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
		Ratio      string `json:"ratio"` // Effective rate with fees included
	} `json:"rate"`
	Calculation struct {
		OutputAsset string `json:"outputAsset"`
	} `json:"calculation"`
	Limit struct {
		Min *float64 `json:"min"`
		Max *float64 `json:"max"`
	} `json:"limit"`
	OperationStatus struct {
		Enabled bool   `json:"enabled"`
		Status  string `json:"status"`
	} `json:"operationStatus"`
}

// GetWhitebirdRateForAmount fetches the Whitebird exchange rate for a specific amount.
// This is essential because Whitebird rates are non-linear (vary with amount).
// Returns the amount of target currency received (not the rate).
func (ac *APICache) GetWhitebirdRateForAmount(from, to string, amount float64) (float64, error) {
	// FIXED: Validate amount before making API call
	if err := ValidateAmount(amount); err != nil {
		return 0, fmt.Errorf("invalid amount: %w", err)
	}

	if !whitebirdCircuit.CanAttempt() {
		ac.mu.Lock()
		ac.whitebirdStatus.Available = false
		ac.mu.Unlock()
		return 0, fmt.Errorf("whitebird service temporarily unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), whitebirdAPITimeout)
	defer cancel()

	outputAmount, err := ac.fetchSingleWhitebirdConversion(ctx, from, to, amount)
	if err != nil {
		whitebirdCircuit.RecordFailure()
		ac.mu.Lock()
		ac.whitebirdStatus.Available = false
		ac.whitebirdStatus.LastError = err
		ac.whitebirdStatus.ConsecutiveFails++
		ac.mu.Unlock()
		return 0, fmt.Errorf("failed to get exchange rate: %w", err)
	}

	whitebirdCircuit.RecordSuccess()
	ac.mu.Lock()
	ac.whitebirdStatus.Available = true
	ac.whitebirdStatus.LastError = nil
	ac.whitebirdStatus.ConsecutiveFails = 0
	ac.whitebirdStatus.LastUpdate = time.Now()
	ac.mu.Unlock()

	return outputAmount, nil
}

func (ac *APICache) fetchSingleWhitebirdConversion(ctx context.Context, from, to string, amount float64) (float64, error) {
	if err := whitebirdLimiter.Wait(ctx); err != nil {
		return 0, err
	}

	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
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

	// Limit response body size
	limitedReader := io.LimitReader(resp.Body, maxHTTPResponseSize)

	var wbResp whitebirdResponse
	if err := json.NewDecoder(limitedReader).Decode(&wbResp); err != nil {
		return 0, fmt.Errorf("failed to decode response: %w", err)
	}

	// Check if operation is enabled first (fail fast)
	if !wbResp.OperationStatus.Enabled {
		return 0, fmt.Errorf("operation not enabled: %s", wbResp.OperationStatus.Status)
	}

	// Validate response
	if wbResp.Calculation.OutputAsset == "" {
		return 0, fmt.Errorf("empty output asset in response")
	}

	// Check amount limits if present
	if wbResp.Limit.Min != nil && amount < *wbResp.Limit.Min {
		return 0, fmt.Errorf("amount %.2f is below minimum limit %.2f", amount, *wbResp.Limit.Min)
	}
	if wbResp.Limit.Max != nil && amount > *wbResp.Limit.Max {
		return 0, fmt.Errorf("amount %.2f exceeds maximum limit %.2f", amount, *wbResp.Limit.Max)
	}

	outputAmount, err := strconv.ParseFloat(wbResp.Calculation.OutputAsset, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid output amount: %s", wbResp.Calculation.OutputAsset)
	}

	if !isValidFloat(outputAmount) || outputAmount <= 0 {
		return 0, fmt.Errorf("invalid output amount: %f", outputAmount)
	}

	// Log the conversion for debugging
	log.Printf("Whitebird %s->%s: input=%.6f, output=%.6f", from, to, amount, outputAmount)

	return outputAmount, nil
}
