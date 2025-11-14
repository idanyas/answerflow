package currency

import (
	"fmt"
	"math"
)

func (ac *APICache) CalculateAverageExecutionPrice(symbol string, amount float64, isBuy bool) (float64, error) {
	if !isValidFloat(amount) {
		return 0, fmt.Errorf("invalid amount")
	}

	ac.mu.RLock()
	rate, ok := ac.bybitRates[symbol]
	if !ok || rate == nil {
		ac.mu.RUnlock()
		return 0, fmt.Errorf("rate not available")
	}

	var orderBook [][]float64
	if isBuy {
		orderBook = rate.OrderBookAsks
	} else {
		orderBook = rate.OrderBookBids
	}

	orderBookCopy := make([][]float64, len(orderBook))
	for i, level := range orderBook {
		if len(level) >= 2 {
			orderBookCopy[i] = []float64{level[0], level[1]}
		}
	}
	ac.mu.RUnlock()

	if len(orderBookCopy) == 0 {
		return 0, fmt.Errorf("empty order book")
	}

	totalFilled := 0.0
	totalCost := 0.0

	for _, level := range orderBookCopy {
		if len(level) < 2 {
			continue
		}

		price, size := level[0], level[1]
		if !isValidFloat(price) || !isValidFloat(size) {
			continue
		}

		if totalFilled+size <= amount {
			totalFilled += size
			totalCost += price * size
		} else {
			remaining := amount - totalFilled
			totalCost += price * remaining
			totalFilled = amount
			break
		}

		if floatGreaterOrEqual(totalFilled, amount) {
			break
		}
	}

	tolerance := liquidityToleranceRelaxed
	if amount > minLargeOrderUSDT {
		tolerance = liquidityToleranceStrict
	}

	if totalFilled < amount*tolerance {
		return 0, fmt.Errorf("insufficient liquidity")
	}

	if !isValidFloat(totalFilled) {
		return 0, fmt.Errorf("no liquidity")
	}

	avgPrice := totalCost / totalFilled
	if !isValidFloat(avgPrice) {
		return 0, fmt.Errorf("invalid price")
	}

	return avgPrice, nil
}

func (ac *APICache) CalculateBuyAmountWithUSDT(symbol string, usdtAmount float64) (float64, float64, error) {
	if !isValidFloat(usdtAmount) {
		return 0, 0, fmt.Errorf("invalid amount")
	}

	ac.mu.RLock()
	rate, ok := ac.bybitRates[symbol]
	if !ok || rate == nil {
		ac.mu.RUnlock()
		return 0, 0, fmt.Errorf("rate not available")
	}

	orderBook := rate.OrderBookAsks
	orderBookCopy := make([][]float64, len(orderBook))
	for i, level := range orderBook {
		if len(level) >= 2 {
			orderBookCopy[i] = []float64{level[0], level[1]}
		}
	}
	ac.mu.RUnlock()

	if len(orderBookCopy) == 0 {
		return 0, 0, fmt.Errorf("empty order book")
	}

	totalUSDTSpent := 0.0
	totalCryptoReceived := 0.0

	for _, level := range orderBookCopy {
		if len(level) < 2 {
			continue
		}

		price, size := level[0], level[1]
		if !isValidFloat(price) || !isValidFloat(size) {
			continue
		}

		levelCost := price * size

		if totalUSDTSpent+levelCost <= usdtAmount {
			totalUSDTSpent += levelCost
			totalCryptoReceived += size
		} else {
			remaining := usdtAmount - totalUSDTSpent
			totalCryptoReceived += remaining / price
			totalUSDTSpent = usdtAmount
			break
		}

		if floatGreaterOrEqual(totalUSDTSpent, usdtAmount) {
			break
		}
	}

	if totalUSDTSpent < usdtAmount*liquidityToleranceRelaxed {
		if isValidFloat(totalCryptoReceived) && totalCryptoReceived > 0 {
			avgPrice := totalUSDTSpent / totalCryptoReceived
			return totalCryptoReceived, avgPrice, nil
		}
		return 0, 0, fmt.Errorf("insufficient liquidity")
	}

	if !isValidFloat(totalCryptoReceived) || totalCryptoReceived <= 0 {
		return 0, 0, fmt.Errorf("no liquidity")
	}

	avgPrice := totalUSDTSpent / totalCryptoReceived
	return totalCryptoReceived, avgPrice, nil
}

func (ac *APICache) CalculateSlippage(symbol string, amount float64, isBuy bool) (float64, error) {
	avgPrice, err := ac.CalculateAverageExecutionPrice(symbol, amount, isBuy)
	if err != nil {
		return 0, err
	}

	ac.mu.RLock()
	rate, ok := ac.bybitRates[symbol]
	if !ok || rate == nil {
		ac.mu.RUnlock()
		return 0, fmt.Errorf("rate not available")
	}

	var bestPrice float64
	if isBuy {
		bestPrice = rate.BestAsk
	} else {
		bestPrice = rate.BestBid
	}
	ac.mu.RUnlock()

	if !isValidFloat(bestPrice) {
		return 0, fmt.Errorf("invalid price")
	}

	return math.Abs((avgPrice-bestPrice)/bestPrice) * 100, nil
}
