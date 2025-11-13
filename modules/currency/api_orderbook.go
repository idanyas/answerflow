package currency

import (
	"fmt"
	"math"
)

// CalculateAverageExecutionPrice calculates the average price for executing a trade of given size
func (ac *APICache) CalculateAverageExecutionPrice(symbol string, amount float64, isBuy bool) (float64, error) {
	if !isValidFloat(amount) {
		return 0, fmt.Errorf("invalid amount: %f", amount)
	}

	ac.mu.RLock()
	rate, ok := ac.bybitRates[symbol]
	if !ok || rate == nil {
		ac.mu.RUnlock()
		return 0, fmt.Errorf("rate not available for %s", symbol)
	}

	var orderBook [][]float64
	if isBuy {
		orderBook = rate.OrderBookAsks
	} else {
		orderBook = rate.OrderBookBids
	}

	// Make defensive copy while holding lock
	orderBookCopy := make([][]float64, len(orderBook))
	for i, level := range orderBook {
		if len(level) >= 2 {
			orderBookCopy[i] = []float64{level[0], level[1]}
		}
	}
	ac.mu.RUnlock()

	if len(orderBookCopy) == 0 {
		return 0, fmt.Errorf("empty order book for %s", symbol)
	}

	totalFilled := 0.0
	totalCost := 0.0

	for _, level := range orderBookCopy {
		if len(level) < 2 {
			continue
		}

		price := level[0]
		size := level[1]

		if !isValidFloat(price) || !isValidFloat(size) {
			continue
		}

		if totalFilled+size <= amount {
			// Can fill entire level
			totalFilled += size
			totalCost += price * size
		} else {
			// Partial fill of this level
			remaining := amount - totalFilled
			totalCost += price * remaining
			totalFilled = amount
			break
		}

		if floatGreaterOrEqual(totalFilled, amount) {
			break
		}
	}

	// Use relaxed tolerance for less critical conversions
	tolerance := liquidityToleranceRelaxed
	if amount > minLargeOrderUSDT {
		tolerance = liquidityToleranceStrict
	}

	if totalFilled < amount*tolerance {
		return 0, fmt.Errorf("insufficient liquidity: only %.2f of %.2f available", totalFilled, amount)
	}

	if !isValidFloat(totalFilled) {
		return 0, fmt.Errorf("no liquidity available")
	}

	averagePrice := totalCost / totalFilled
	if !isValidFloat(averagePrice) {
		return 0, fmt.Errorf("invalid average price calculated")
	}

	return averagePrice, nil
}

// CalculateBuyAmountWithUSDT calculates how much crypto can be bought with a specific USDT amount.
func (ac *APICache) CalculateBuyAmountWithUSDT(symbol string, usdtAmount float64) (cryptoAmount float64, avgPrice float64, err error) {
	if !isValidFloat(usdtAmount) {
		return 0, 0, fmt.Errorf("invalid USDT amount: %f", usdtAmount)
	}

	ac.mu.RLock()
	rate, ok := ac.bybitRates[symbol]
	if !ok || rate == nil {
		ac.mu.RUnlock()
		return 0, 0, fmt.Errorf("rate not available for %s", symbol)
	}

	// Make defensive copy while holding lock
	orderBook := rate.OrderBookAsks // We're buying, so we look at asks
	orderBookCopy := make([][]float64, len(orderBook))
	for i, level := range orderBook {
		if len(level) >= 2 {
			orderBookCopy[i] = []float64{level[0], level[1]}
		}
	}
	ac.mu.RUnlock()

	if len(orderBookCopy) == 0 {
		return 0, 0, fmt.Errorf("empty ask order book for %s", symbol)
	}

	totalUSDTSpent := 0.0
	totalCryptoReceived := 0.0

	for _, level := range orderBookCopy {
		if len(level) < 2 {
			continue
		}

		price := level[0]
		size := level[1]

		if !isValidFloat(price) || !isValidFloat(size) {
			continue
		}

		levelCostUSDT := price * size

		if totalUSDTSpent+levelCostUSDT <= usdtAmount {
			// Can buy entire level
			totalUSDTSpent += levelCostUSDT
			totalCryptoReceived += size
		} else {
			// Partial fill of this level
			remainingUSDT := usdtAmount - totalUSDTSpent
			partialCrypto := remainingUSDT / price
			totalCryptoReceived += partialCrypto
			totalUSDTSpent = usdtAmount
			break
		}

		if floatGreaterOrEqual(totalUSDTSpent, usdtAmount) {
			break
		}
	}

	// Use relaxed tolerance for buying
	if totalUSDTSpent < usdtAmount*liquidityToleranceRelaxed {
		// Try to use whatever we could get
		if isValidFloat(totalCryptoReceived) && totalCryptoReceived > 0 {
			avgPrice = totalUSDTSpent / totalCryptoReceived
			return totalCryptoReceived, avgPrice, nil
		}
		return 0, 0, fmt.Errorf("insufficient liquidity: only %.2f USDT of %.2f could be spent", totalUSDTSpent, usdtAmount)
	}

	if !isValidFloat(totalCryptoReceived) || totalCryptoReceived <= 0 {
		return 0, 0, fmt.Errorf("no liquidity available")
	}

	avgPrice = totalUSDTSpent / totalCryptoReceived
	if !isValidFloat(avgPrice) {
		return 0, 0, fmt.Errorf("invalid average price")
	}

	return totalCryptoReceived, avgPrice, nil
}

// CalculateSlippage returns slippage as a percentage
func (ac *APICache) CalculateSlippage(symbol string, amount float64, isBuy bool) (float64, error) {
	if !isValidFloat(amount) {
		return 0, fmt.Errorf("invalid amount: %f", amount)
	}

	avgPrice, err := ac.CalculateAverageExecutionPrice(symbol, amount, isBuy)
	if err != nil {
		return 0, err
	}

	ac.mu.RLock()
	rate, ok := ac.bybitRates[symbol]
	if !ok || rate == nil {
		ac.mu.RUnlock()
		return 0, fmt.Errorf("rate not available for %s", symbol)
	}

	var bestPrice float64
	if isBuy {
		bestPrice = rate.BestAsk
	} else {
		bestPrice = rate.BestBid
	}
	ac.mu.RUnlock()

	if !isValidFloat(bestPrice) {
		return 0, fmt.Errorf("invalid best price")
	}

	// Return as percentage
	slippage := math.Abs((avgPrice-bestPrice)/bestPrice) * 100
	return slippage, nil
}
