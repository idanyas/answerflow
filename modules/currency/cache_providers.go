package currency

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

func (ac *APICache) StartBackgroundUpdaters() {
	log.Println("Starting background currency updaters...")
	go ac.updateLoop("bybit", backgroundUpdateTTL, ac.fetchBybitRates, &ac.bybitStatus, &ac.bybitHealthy)
	go ac.updateLoop("mastercard", backgroundUpdateTTL*3, ac.fetchMastercardRates, &ac.mastercardStatus, &ac.mastercardHealthy)
	go ac.startHealthMonitoring()
}

func (ac *APICache) updateLoop(name string, interval time.Duration, fetchFn func() error, status *ProviderStatus, healthFlag *atomic.Bool) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), interval/2)
			err := retryWithBackoff(ctx, fetchFn)
			cancel()

			ac.mu.Lock()
			if err != nil {
				status.Available = false
				status.LastError = err
				status.ConsecutiveFails++
				healthFlag.Store(false)

				if status.ConsecutiveFails >= maxConsecutiveFailures {
					log.Printf("CRITICAL: %s update failed %d consecutive times: %v", name, status.ConsecutiveFails, err)
				}
			} else {
				wasDown := status.ConsecutiveFails > 0
				status.Available = true
				status.LastError = nil
				status.ConsecutiveFails = 0
				status.LastUpdate = time.Now()
				healthFlag.Store(true)

				if wasDown {
					log.Printf("Info: %s service recovered", name)
				}
			}
			ac.mu.Unlock()

			// Save to file after successful update
			if err == nil {
				ac.SaveToFileAsync()
			}

		case <-ac.shutdownChan:
			log.Printf("Shutting down %s update loop", name)
			return
		}
	}
}

func (ac *APICache) ForceRefresh() error {
	if !ac.refreshInProgress.CompareAndSwap(false, true) {
		return fmt.Errorf("refresh already in progress")
	}
	defer ac.refreshInProgress.Store(false)

	log.Println("Force refreshing all rates...")
	var wg sync.WaitGroup
	var errBybit, errMastercard error
	var mu sync.Mutex

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	wg.Add(2)
	go func() {
		defer wg.Done()
		err := retryWithBackoff(ctx, ac.fetchBybitRates)
		mu.Lock()
		errBybit = err
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		err := retryWithBackoff(ctx, ac.fetchMastercardRates)
		mu.Lock()
		errMastercard = err
		mu.Unlock()
	}()

	wg.Wait()

	// Save to file after force refresh
	if errBybit == nil || errMastercard == nil {
		ac.SaveToFileAsync()
	}

	if errBybit != nil {
		return fmt.Errorf("failed to force refresh bybit rates: %w", errBybit)
	}
	if errMastercard != nil {
		return fmt.Errorf("failed to force refresh mastercard rates: %w", errMastercard)
	}
	return nil
}
