package currency

// This file contains the lists of supported crypto and fiat currencies.

// Crypto currencies supported and fetched from Bybit
var supportedCryptos = []string{
	"TON", "BTC", "ETH", "SOL", "ADA", "DOGE", "XRP", "DOT", "LINK", "UNI",
	"ATOM", "AVAX", "NEAR", "APT", "ARB", "OP", "USDT",
}

// Fiat currencies supported by Mastercard
var supportedFiats = []string{
	"AFN", "ALL", "DZD", "AOA", "ARS", "AMD", "AWG", "AUD", "AZN", "BSD", "BHD", "BDT", "BBD", "BYN", "BZD",
	"BMD", "BTN", "BOB", "BAM", "BWP", "BRL", "BND", "BGN", "BIF", "KHR", "CAD", "CVE", "XCG", "KYD", "XOF",
	"XAF", "XPF", "CLP", "CNY", "COP", "KMF", "CDF", "CRC", "CUP", "CZK", "DKK", "DJF", "DOP", "XCD", "EGP",
	"SVC", "ETB", "EUR", "FKP", "FJD", "GMD", "GEL", "GHS", "GIP", "GBP", "GTQ", "GNF", "GYD", "HTG", "HNL",
	"HKD", "HUF", "ISK", "INR", "IDR", "IQD", "ILS", "JMD", "JPY", "JOD", "KZT", "KES", "KWD", "KGS", "LAK",
	"LBP", "LSL", "LRD", "LYD", "MOP", "MKD", "MGA", "MWK", "MYR", "MVR", "MRU", "MUR", "MXN", "MDL", "MNT",
	"MAD", "MZN", "MMK", "NAD", "NPR", "NZD", "NIO", "NGN", "NOK", "OMR", "PKR", "PAB", "PGK", "PYG", "PEN",
	"PHP", "PLN", "QAR", "RON", "RWF", "SHP", "WST", "STN", "SAR", "RSD", "SCR", "SLE", "SGD", "SBD", "SOS",
	"ZAR", "KRW", "SSP", "LKR", "SDG", "SRD", "SZL", "SEK", "CHF", "TWD", "TJS", "TZS", "THB", "TOP", "TTD",
	"TND", "TRY", "TMT", "UGX", "UAH", "AED", "USD", "UYU", "UZS", "VUV", "VES", "VND", "YER", "ZMW", "ZWG",
	"RUB", // Add RUB to fiat list
}

// Priority fiat currencies for initial fetch
var priorityFiats = []string{"EUR", "GBP", "JPY", "CNY", "CHF", "CAD", "AUD", "UAH", "TRY", "KRW"}
