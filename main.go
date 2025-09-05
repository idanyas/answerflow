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
	"answerflow/modules/calculator"
	"answerflow/modules/currency"
)

const (
	httpPort             = ":8080"
	requestTimeout       = 5 * time.Second // Overall request timeout
	defaultModuleIcon    = "https://img.icons8.com/badges/100/decision.png"
	noResultsIconPath    = "https://img.icons8.com/badges/100/decision.png"
	currencyModuleIcon   = "https://img.icons8.com/badges/100/euro-exchange.png"
	calculatorModuleIcon = "https://img.icons8.com/badges/100/calculator.png"
)

var (
	registeredModules []modules.Module
	globalAPICache    *currency.APICache
)

func main() {
	// Initialize the new proactive API cache
	globalAPICache = currency.NewAPICache()
	// Perform a blocking initial fetch to ensure data is ready before serving requests.
	log.Println("Performing initial fetch of currency data...")
	if err := globalAPICache.InitialFetch(); err != nil {
		log.Fatalf("Failed to perform initial data fetch: %v", err)
	}
	log.Println("Initial data fetch complete.")

	// Start the background processes to keep the cache updated
	globalAPICache.StartBackgroundUpdaters()

	currencyModuleInstance := currency.NewCurrencyConverterModule(
		[]string{"USD", "EUR", "KZT"}, // Quick conversion targets
		"RUB",                         // Base conversion currency
		currencyModuleIcon,
		true, // ShortDisplayFormat: false means "Input = Output", true means "Output" only
	)
	registeredModules = append(registeredModules, currencyModuleInstance)

	calculatorModuleInstance := calculator.NewCalculatorModule(calculatorModuleIcon)
	registeredModules = append(registeredModules, calculatorModuleInstance)

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
			// The context passed to modules is for the overall request timeout,
			// but individual modules might not need it if data is pre-cached.
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
				if res.IcoPath == "" { // Fallback if module's default icon is also empty
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
		// If timeout occurs, allResults might be partially populated or empty.
		// We still proceed to check if it's empty below.
	}

	sort.SliceStable(allResults, func(i, j int) bool {
		return allResults[i].Score > allResults[j].Score
	})

	// MODIFIED: Check if allResults is empty and provide a "no results" item.
	if len(allResults) == 0 && query != "" { // Only show "no results" if there was an actual query.
		noResultsItem := commontypes.FlowResult{
			Title:    "No results found",
			SubTitle: "Please try a different query.",
			IcoPath:  noResultsIconPath,
			Score:    0, // Will be the only item, score is less critical here.
			JsonRPCAction: commontypes.JsonRPCAction{
				Method:     "Flow.Launcher.ChangeQuery", // Standard Flow Launcher method
				Parameters: []interface{}{query, false}, // Parameters: queryText (string), requery (bool)
			},
		}
		allResults = append(allResults, noResultsItem)
	} else if len(allResults) == 0 && query == "" {
		// If query is empty and no results (e.g. initial plugin load view), return an empty list.
		// Or, you could define a specific "welcome" or "type to search" message here.
		// For now, stick to Flow Launcher's default for an empty query by sending an empty list.
		allResults = []commontypes.FlowResult{}
	}
	// END MODIFIED

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(allResults); err != nil {
		log.Printf("Error encoding JSON response: %v", err)
	}
}
