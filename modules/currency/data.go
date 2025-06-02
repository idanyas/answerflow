package currency

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	symbolsConfigPath     = "modules/currency/config/currency_symbols.json"
	nameAliasesConfigPath = "modules/currency/config/currency_name_aliases.json"
)

// CurrencyData holds mappings for symbols, names, and codes.
type CurrencyData struct {
	symbols     map[string]string // symbol -> canonical code (e.g. "$" -> "USD")
	nameAliases map[string]string // lowercase name/alias -> canonical code (e.g. "dollar" -> "USD")
	validCodes  map[string]string // lowercase code -> canonical code (e.g. "usd" -> "USD")
	mu          sync.RWMutex
	initialised bool // Tracks if PopulateDynamicAliases has been run with API data at least once
}

func loadConfigMapFromFile(filePath string) (map[string]string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		// Try to construct path relative to current working directory if direct path fails
		cwd, _ := os.Getwd()
		altPath := filepath.Join(cwd, filePath)
		data, err = os.ReadFile(altPath)
		if err != nil {
			return nil, fmt.Errorf("reading config file %s (and alternative %s): %w", filePath, altPath, err)
		}
		log.Printf("Loaded config file from alternative path: %s", altPath)
	}

	var configMap map[string]string
	if err := json.Unmarshal(data, &configMap); err != nil {
		return nil, fmt.Errorf("unmarshaling config file %s: %w", filePath, err)
	}
	return configMap, nil
}

func NewCurrencyData() *CurrencyData {
	// Load symbols from JSON
	loadedSymbols, err := loadConfigMapFromFile(symbolsConfigPath)
	if err != nil {
		log.Printf("Warning: Failed to load currency symbols from %s: %v. Using empty map.", symbolsConfigPath, err)
		loadedSymbols = make(map[string]string)
	}

	// Load name aliases from JSON
	loadedNameAliases, err := loadConfigMapFromFile(nameAliasesConfigPath)
	if err != nil {
		log.Printf("Warning: Failed to load currency name aliases from %s: %v. Using empty map.", nameAliasesConfigPath, err)
		loadedNameAliases = make(map[string]string)
	}

	cd := &CurrencyData{
		symbols:     loadedSymbols,
		nameAliases: loadedNameAliases, // Will be processed for case and content
		validCodes:  make(map[string]string),
		initialised: false,
	}

	// Process loaded symbols: ensure values are uppercase canonical codes
	// and populate validCodes from these symbols.
	processedSymbols := make(map[string]string)
	for symbol, code := range cd.symbols {
		canonicalCode := strings.ToUpper(code)
		processedSymbols[symbol] = canonicalCode // Keep original symbol casing for key

		lcCanonicalCode := strings.ToLower(canonicalCode)
		if _, exists := cd.validCodes[lcCanonicalCode]; !exists {
			cd.validCodes[lcCanonicalCode] = canonicalCode
		}
	}
	cd.symbols = processedSymbols

	// Process loaded name aliases: ensure keys are lowercase and values are uppercase canonical codes.
	// Populate validCodes from these aliases.
	processedNameAliases := make(map[string]string)
	for alias, code := range cd.nameAliases {
		lcAlias := strings.ToLower(alias)
		canonicalCode := strings.ToUpper(code)
		processedNameAliases[lcAlias] = canonicalCode

		lcCanonicalCode := strings.ToLower(canonicalCode)
		if _, exists := cd.validCodes[lcCanonicalCode]; !exists {
			cd.validCodes[lcCanonicalCode] = canonicalCode
		}
		if len(lcAlias) >= 2 && len(lcAlias) <= 10 {
			isPotentialCode := true
			for _, char := range lcAlias {
				if !(char >= 'a' && char <= 'z') {
					isPotentialCode = false
					break
				}
			}
			if isPotentialCode {
				if _, exists := cd.validCodes[lcAlias]; !exists {
					cd.validCodes[lcAlias] = canonicalCode
				}
			}
		}
	}
	cd.nameAliases = processedNameAliases
	return cd
}

func (cd *CurrencyData) PopulateDynamicAliases(allCurrenciesFromAPI map[string]string) {
	cd.mu.Lock()
	defer cd.mu.Unlock()

	if len(allCurrenciesFromAPI) == 0 && cd.initialised {
		return
	}

	for apiKey, fullName := range allCurrenciesFromAPI {
		lcAPIKey := strings.ToLower(apiKey)
		canonicalCode := strings.ToUpper(apiKey)

		if existingCanonical, ok := cd.validCodes[lcAPIKey]; ok {
			canonicalCode = existingCanonical
		} else {
			cd.validCodes[lcAPIKey] = canonicalCode
		}

		if fullName != "" {
			lcFullName := strings.ToLower(fullName)
			if existingAliasCanonical, exists := cd.nameAliases[lcFullName]; !exists || existingAliasCanonical != canonicalCode {
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
		return "", fmt.Errorf("cannot resolve empty currency string")
	}

	// 1. Check symbols (original case, then lowercase)
	if code, ok := cd.symbols[sTrimmed]; ok {
		return code, nil
	}
	if sTrimmed != sLower {
		if code, ok := cd.symbols[sLower]; ok {
			return code, nil
		}
	}

	// 2. Check validCodes (lowercase keys) - these are canonical codes known from JSON values or API
	if code, ok := cd.validCodes[sLower]; ok {
		return code, nil
	}

	// 3. Check nameAliases (lowercase keys) - these are aliases from JSON files or API full names
	if code, ok := cd.nameAliases[sLower]; ok {
		return code, nil
	}

	// 4. Fallback: if sTrimmed (original case) looks like a code, assume it is.
	//    This helps with unlisted codes or codes typed in mixed/non-standard case.
	if len(sTrimmed) >= 2 && len(sTrimmed) <= 10 {
		isAlpha := true
		allUpper := true
		// No need to check allLower here for this specific fallback logic.
		// The primary lookups (validCodes, nameAliases) use sLower for case-insensitivity.
		for _, r := range sTrimmed {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
				isAlpha = false
				break
			}
			if !(r >= 'A' && r <= 'Z') {
				allUpper = false
			}
		}

		// Condition:
		// 1. All characters are alphabetic.
		// 2. EITHER the string is exactly 3 characters long (common for ISO codes, allows mixed case like "uSd")
		// 3. OR the string is all uppercase (catches codes like "BITCOIN" if not already known).
		if isAlpha && (len(sTrimmed) == 3 || allUpper) {
			// log.Printf("Dynamically recognizing '%s' as currency code '%s' via fallback.", sTrimmed, strings.ToUpper(sTrimmed))
			return strings.ToUpper(sTrimmed), nil
		}
	}

	return "", fmt.Errorf("unknown or ambiguous currency '%s'", sTrimmed)
}

func (cd *CurrencyData) ExtractSymbol(currStrCandidate, amountStr string) (string_currency_code_candidate string, string_amount string) {
	cd.mu.RLock()
	defer cd.mu.RUnlock()

	amountStr = strings.TrimSpace(amountStr)
	originalCurrStrCandidate := currStrCandidate

	if resolvedCode, err := cd.ResolveCurrency(currStrCandidate); err == nil {
		return resolvedCode, amountStr
	}

	bestPrefixMatch := ""
	bestPrefixCode := ""
	for sym, code := range cd.symbols {
		if strings.HasPrefix(amountStr, sym) {
			if len(sym) > len(bestPrefixMatch) {
				bestPrefixMatch = sym
				bestPrefixCode = code
			}
		}
	}
	if bestPrefixMatch != "" {
		newAmountStr := strings.TrimSpace(strings.TrimPrefix(amountStr, bestPrefixMatch))
		return bestPrefixCode, newAmountStr
	}

	bestSuffixMatch := ""
	bestSuffixCode := ""
	for sym, code := range cd.symbols {
		if strings.HasSuffix(amountStr, sym) {
			if len(sym) > len(bestSuffixMatch) {
				bestSuffixMatch = sym
				bestSuffixCode = code
			}
		}
	}
	if bestSuffixMatch != "" {
		newAmountStr := strings.TrimSpace(strings.TrimSuffix(amountStr, bestSuffixMatch))
		return bestSuffixCode, newAmountStr
	}

	if resolvedOriginal, err := cd.ResolveCurrency(originalCurrStrCandidate); err == nil {
		return resolvedOriginal, amountStr
	}

	return originalCurrStrCandidate, amountStr
}
