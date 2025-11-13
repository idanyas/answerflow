package currency

import "time"

type CurrencyMetadata struct {
	DecimalPlaces      int
	MinTradingAmount   float64
	MaxTradingAmount   float64
	IsTradeableOnBybit bool
	LastVerified       time.Time
}

type BybitRate struct {
	BestBid       float64
	BestAsk       float64
	OrderBookBids [][]float64
	OrderBookAsks [][]float64
	LastUpdate    time.Time
	Volume24h     float64
}
