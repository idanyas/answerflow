package currency

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	persistenceFilePath = "data/exchange_rates.json"
	persistenceVersion  = "1.0"
)

type PersistedCache struct {
	Version          string                `json:"version"`
	LastUpdated      time.Time             `json:"last_updated"`
	BybitLastUpdate  time.Time             `json:"bybit_last_update"`
	MastercardUpdate time.Time             `json:"mastercard_last_update"`
	BybitRates       map[string]*BybitRate `json:"bybit_rates"`
	MastercardRates  map[string]float64    `json:"mastercard_rates"`
}

var (
	saveMutex       sync.Mutex
	lastSaveTime    time.Time
	minSaveInterval = 30 * time.Second // Don't save more often than every 30 seconds
)

// LoadFromFile attempts to load previously saved exchange rates from disk
func (ac *APICache) LoadFromFile() error {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	file, err := os.Open(persistenceFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("No persisted cache file found, will fetch fresh data")
			return nil
		}
		return fmt.Errorf("failed to open cache file: %w", err)
	}
	defer file.Close()

	var persisted PersistedCache
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&persisted); err != nil {
		return fmt.Errorf("failed to decode cache file: %w", err)
	}

	// Validate version
	if persisted.Version != persistenceVersion {
		log.Printf("Warning: Cache file version mismatch (expected %s, got %s)", persistenceVersion, persisted.Version)
		return nil
	}

	// Check if data is too old (more than 24 hours)
	if time.Since(persisted.LastUpdated) > 24*time.Hour {
		log.Printf("Warning: Cached data is %v old, will fetch fresh data", time.Since(persisted.LastUpdated))
		return nil
	}

	// Load Bybit rates
	if len(persisted.BybitRates) > 0 {
		ac.bybitRates = persisted.BybitRates
		ac.lastBybitRates = make(map[string]*BybitRate)
		for k, v := range persisted.BybitRates {
			ac.lastBybitRates[k] = v
			ac.tradeablePairs[k] = true
		}
		ac.bybitLastUpdate = persisted.BybitLastUpdate
		ac.bybitStatus.Available = true
		ac.bybitStatus.LastUpdate = persisted.BybitLastUpdate
		ac.bybitHealthy.Store(true)
		log.Printf("Loaded %d Bybit rates from cache (last updated: %v ago)",
			len(ac.bybitRates), time.Since(persisted.BybitLastUpdate))
	}

	// Load Mastercard rates
	if len(persisted.MastercardRates) > 0 {
		ac.mastercardRates = persisted.MastercardRates
		ac.lastMastercardRates = make(map[string]float64)
		for k, v := range persisted.MastercardRates {
			ac.lastMastercardRates[k] = v
		}
		ac.mastercardLastUpdate = persisted.MastercardUpdate
		ac.mastercardStatus.Available = true
		ac.mastercardStatus.LastUpdate = persisted.MastercardUpdate
		ac.mastercardHealthy.Store(true)
		log.Printf("Loaded %d Mastercard rates from cache (last updated: %v ago)",
			len(ac.mastercardRates), time.Since(persisted.MastercardUpdate))
	}

	log.Printf("Successfully loaded exchange rates from cache file (saved %v ago)", time.Since(persisted.LastUpdated))
	return nil
}

// SaveToFile saves current exchange rates to disk
func (ac *APICache) SaveToFile() error {
	// Rate limiting: don't save too frequently
	saveMutex.Lock()
	if time.Since(lastSaveTime) < minSaveInterval {
		saveMutex.Unlock()
		return nil // Skip save, too soon
	}
	lastSaveTime = time.Now()
	saveMutex.Unlock()

	ac.mu.RLock()

	// Create persistence structure
	persisted := PersistedCache{
		Version:          persistenceVersion,
		LastUpdated:      time.Now(),
		BybitLastUpdate:  ac.bybitLastUpdate,
		MastercardUpdate: ac.mastercardLastUpdate,
		BybitRates:       make(map[string]*BybitRate),
		MastercardRates:  make(map[string]float64),
	}

	// Copy Bybit rates
	for k, v := range ac.bybitRates {
		if v != nil {
			persisted.BybitRates[k] = v
		}
	}

	// Copy Mastercard rates
	for k, v := range ac.mastercardRates {
		persisted.MastercardRates[k] = v
	}

	ac.mu.RUnlock()

	// Ensure directory exists
	dir := filepath.Dir(persistenceFilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write to temporary file first
	tempFile := persistenceFilePath + ".tmp"
	file, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")

	if err := encoder.Encode(persisted); err != nil {
		file.Close()
		os.Remove(tempFile)
		return fmt.Errorf("failed to encode cache: %w", err)
	}

	if err := file.Close(); err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tempFile, persistenceFilePath); err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	log.Printf("Saved %d Bybit rates and %d Mastercard rates to %s",
		len(persisted.BybitRates), len(persisted.MastercardRates), persistenceFilePath)

	return nil
}

// SaveToFileAsync saves to file in a goroutine, logging errors but not blocking
func (ac *APICache) SaveToFileAsync() {
	go func() {
		if err := ac.SaveToFile(); err != nil {
			log.Printf("Warning: Failed to save cache to file: %v", err)
		}
	}()
}
