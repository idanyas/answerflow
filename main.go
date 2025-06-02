package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"answerflow/commontypes"
	"answerflow/modules"
	"answerflow/modules/currency"
)

const (
	httpPort          = ":8080"
	requestTimeout    = 5 * time.Second // Overall request timeout
	defaultModuleIcon = "Images/default.png"
)

var (
	registeredModules []modules.Module
	globalAPICache    *currency.APICache
)

func main() {
	globalAPICache = currency.NewAPICache(2*time.Minute, 2*time.Minute)

	currencyModuleInstance := currency.NewCurrencyConverterModule(
		[]string{"USD", "EUR", "KZT"}, // Quick conversion targets
		"RUB",                         // Base conversion currency
		"https://img.icons8.com/badges/100/euro-exchange.png", // Default icon for currency module
		true, // ShortDisplayFormat: false means "Input = Output", true means "Output" only
	)
	registeredModules = append(registeredModules, currencyModuleInstance)

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleQuery)

	server := &http.Server{
		Addr:         httpPort,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("Flow HTTP Receiver listening on port %s at path /", httpPort)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Could not listen on %s: %v\n", httpPort, err)
	}
}

func handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	query := r.URL.Query().Get("q")

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var allResults []commontypes.FlowResult
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, mod := range registeredModules {
		wg.Add(1)
		go func(m modules.Module) {
			defer wg.Done()
			// Create a new context for each module if needed, or use the shared one.
			// Using shared ctx ensures module processing adheres to overall timeout.
			moduleCtx := ctx

			results, err := m.ProcessQuery(moduleCtx, query, globalAPICache)
			if err != nil {
				log.Printf("Module '%s' failed for query '%s': %v", m.Name(), query, err)
				return
			}

			mu.Lock()
			for _, res := range results {
				if res.IcoPath == "" {
					res.IcoPath = m.DefaultIconPath()
				}
				if res.IcoPath == "" {
					res.IcoPath = defaultModuleIcon
				}
				allResults = append(allResults, res)
			}
			mu.Unlock()
		}(mod)
	}

	waitChan := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitChan)
	}()

	select {
	case <-waitChan:
		// All goroutines completed within timeout
	case <-ctx.Done():
		// Timeout reached or context canceled
		log.Printf("Request processing timed out or was canceled for query: '%s', error: %v", query, ctx.Err())
	}

	sort.SliceStable(allResults, func(i, j int) bool {
		return allResults[i].Score > allResults[j].Score
	})

	if allResults == nil {
		allResults = []commontypes.FlowResult{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(allResults); err != nil {
		log.Printf("Error encoding JSON response: %v", err)
	}
}
