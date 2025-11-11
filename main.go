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
	requestTimeout       = 5 * time.Second
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
	globalAPICache = currency.NewAPICache()
	log.Println("Performing initial fetch of currency data...")
	if err := globalAPICache.InitialFetch(); err != nil {
		log.Fatalf("Failed to perform initial data fetch: %v", err)
	}
	log.Println("Initial data fetch complete.")

	globalAPICache.StartBackgroundUpdaters()

	currencyModuleInstance := currency.NewCurrencyConverterModule(
		[]string{"EUR"}, // Quick conversion targets (EUR only, RUB/USD handled specially)
		"USD",           // Base conversion currency
		currencyModuleIcon,
		true, // ShortDisplayFormat
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
	case <-ctx.Done():
		log.Printf("Request processing timed out or was canceled for query: '%s', error: %v", query, ctx.Err())
	}

	sort.SliceStable(allResults, func(i, j int) bool {
		return allResults[i].Score > allResults[j].Score
	})

	if len(allResults) == 0 && query != "" {
		noResultsItem := commontypes.FlowResult{
			Title:    "No results found",
			SubTitle: "Please try a different query.",
			IcoPath:  noResultsIconPath,
			Score:    0,
			JsonRPCAction: commontypes.JsonRPCAction{
				Method:     "Flow.Launcher.ChangeQuery",
				Parameters: []interface{}{query, false},
			},
		}
		allResults = append(allResults, noResultsItem)
	} else if len(allResults) == 0 && query == "" {
		allResults = []commontypes.FlowResult{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(allResults); err != nil {
		log.Printf("Error encoding JSON response: %v", err)
	}
}
