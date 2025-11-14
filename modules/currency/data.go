package currency

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
)

//go:embed config/currency_symbols.json
var embeddedSymbolsJSON []byte

//go:embed config/currency_name_aliases.json
var embeddedNameAliasesJSON []byte

type CurrencyData struct {
	symbols     map[string]string
	nameAliases map[string]string
	validCodes  map[string]string
	mu          sync.RWMutex
	initialised bool
}

func NewCurrencyData() *CurrencyData {
	loadedSymbols, err := loadConfigMap(embeddedSymbolsJSON, "symbols")
	if err != nil {
		log.Printf("Warning: Failed to load symbols: %v", err)
		loadedSymbols = make(map[string]string)
	}

	loadedAliases, err := loadConfigMap(embeddedNameAliasesJSON, "aliases")
	if err != nil {
		log.Printf("Warning: Failed to load aliases: %v", err)
		loadedAliases = make(map[string]string)
	}

	cd := &CurrencyData{
		symbols:     make(map[string]string),
		nameAliases: make(map[string]string),
		validCodes:  make(map[string]string),
		initialised: false,
	}

	for symbol, code := range loadedSymbols {
		canonicalCode := strings.ToUpper(code)
		cd.symbols[symbol] = canonicalCode
		cd.validCodes[strings.ToLower(canonicalCode)] = canonicalCode
	}

	for alias, code := range loadedAliases {
		lcAlias := strings.ToLower(alias)
		canonicalCode := strings.ToUpper(code)
		cd.nameAliases[lcAlias] = canonicalCode
		cd.validCodes[strings.ToLower(canonicalCode)] = canonicalCode

		if len(lcAlias) >= 2 && len(lcAlias) <= 10 && isAlpha(lcAlias) {
			cd.validCodes[lcAlias] = canonicalCode
		}
	}

	return cd
}

func loadConfigMap(data []byte, description string) (map[string]string, error) {
	var configMap map[string]string
	if err := json.Unmarshal(data, &configMap); err != nil {
		return nil, fmt.Errorf("unmarshaling %s: %w", description, err)
	}
	return configMap, nil
}

func isAlpha(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}

func (cd *CurrencyData) PopulateDynamicAliases(allCurrencies map[string]string) {
	cd.mu.Lock()
	defer cd.mu.Unlock()

	if len(allCurrencies) == 0 && cd.initialised {
		return
	}

	for apiKey, fullName := range allCurrencies {
		lcAPIKey := strings.ToLower(apiKey)
		canonicalCode := strings.ToUpper(apiKey)

		if existing, ok := cd.validCodes[lcAPIKey]; ok {
			canonicalCode = existing
		} else {
			cd.validCodes[lcAPIKey] = canonicalCode
		}

		if fullName != "" {
			lcFullName := strings.ToLower(fullName)
			if existing, exists := cd.nameAliases[lcFullName]; !exists || existing != canonicalCode {
				cd.nameAliases[lcFullName] = canonicalCode
			}
		}
	}
	cd.initialised = true
}

func (cd *CurrencyData) ResolveCurrency(s string) (string, error) {
	cd.mu.RLock()
	defer cd.mu.RUnlock()

	sTrimmed := strings.TrimSpace(s)
	sLower := strings.ToLower(sTrimmed)

	if sTrimmed == "" {
		return "", fmt.Errorf("empty currency")
	}

	if code, ok := cd.symbols[sTrimmed]; ok {
		return code, nil
	}
	if sTrimmed != sLower {
		if code, ok := cd.symbols[sLower]; ok {
			return code, nil
		}
	}

	if code, ok := cd.validCodes[sLower]; ok {
		return code, nil
	}

	if code, ok := cd.nameAliases[sLower]; ok {
		return code, nil
	}

	if len(sTrimmed) >= 2 && len(sTrimmed) <= 10 && isAlpha(sTrimmed) {
		if len(sTrimmed) == 3 || sTrimmed == strings.ToUpper(sTrimmed) {
			return strings.ToUpper(sTrimmed), nil
		}
	}

	return "", fmt.Errorf("unknown currency '%s'", sTrimmed)
}

func (cd *CurrencyData) ExtractSymbol(currCandidate, amountStr string) (string, string) {
	cd.mu.RLock()
	defer cd.mu.RUnlock()

	amountStr = strings.TrimSpace(amountStr)

	if resolvedCode, err := cd.ResolveCurrency(currCandidate); err == nil {
		return resolvedCode, amountStr
	}

	bestPrefix, bestPrefixCode := "", ""
	for sym, code := range cd.symbols {
		if strings.HasPrefix(amountStr, sym) && len(sym) > len(bestPrefix) {
			bestPrefix, bestPrefixCode = sym, code
		}
	}
	if bestPrefix != "" {
		return bestPrefixCode, strings.TrimSpace(strings.TrimPrefix(amountStr, bestPrefix))
	}

	bestSuffix, bestSuffixCode := "", ""
	for sym, code := range cd.symbols {
		if strings.HasSuffix(amountStr, sym) && len(sym) > len(bestSuffix) {
			bestSuffix, bestSuffixCode = sym, code
		}
	}
	if bestSuffix != "" {
		return bestSuffixCode, strings.TrimSpace(strings.TrimSuffix(amountStr, bestSuffix))
	}

	if resolved, err := cd.ResolveCurrency(currCandidate); err == nil {
		return resolved, amountStr
	}

	return currCandidate, amountStr
}
