package currency

import (
	"log"
	"sync"
	"time"
)

const (
	circuitBreakerThreshold = 5
	circuitBreakerTimeout   = 5 * time.Minute
)

type CircuitBreaker struct {
	mu        sync.RWMutex
	failures  int
	openUntil time.Time
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	if cb.failures >= circuitBreakerThreshold {
		cb.openUntil = time.Now().Add(circuitBreakerTimeout)
		log.Printf("Circuit breaker opened, will retry after %v", circuitBreakerTimeout)
	}
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.failures > 0 {
		cb.failures--
	}
	if time.Now().After(cb.openUntil) {
		cb.openUntil = time.Time{}
	}
}

func (cb *CircuitBreaker) CanAttempt() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return time.Now().After(cb.openUntil)
}

func (cb *CircuitBreaker) GetState() string {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	if time.Now().Before(cb.openUntil) {
		return "open"
	}
	return "closed"
}

var (
	whitebirdCircuit  = &CircuitBreaker{}
	bybitCircuit      = &CircuitBreaker{}
	mastercardCircuit = &CircuitBreaker{}
)

func (ac *APICache) startHealthMonitoring() {
	ac.healthTicker = time.NewTicker(healthCheckInterval)
	defer ac.healthTicker.Stop()

	for {
		select {
		case <-ac.healthTicker.C:
			ac.performHealthCheck()
		case <-ac.healthStopChan:
			return
		case <-ac.shutdownChan:
			return
		}
	}
}

func (ac *APICache) performHealthCheck() {
	ac.mu.RLock()
	bybitFails := ac.bybitStatus.ConsecutiveFails
	mastercardFails := ac.mastercardStatus.ConsecutiveFails
	whitebirdFails := ac.whitebirdStatus.ConsecutiveFails
	ac.mu.RUnlock()

	if bybitFails > 0 || mastercardFails > 0 || whitebirdFails > 0 {
		log.Printf("Health check: Bybit=%d fails, Mastercard=%d fails, Whitebird=%d fails",
			bybitFails, mastercardFails, whitebirdFails)
	}

	if !bybitCircuit.CanAttempt() {
		log.Printf("Health check: Bybit circuit breaker is %s", bybitCircuit.GetState())
	}
	if !mastercardCircuit.CanAttempt() {
		log.Printf("Health check: Mastercard circuit breaker is %s", mastercardCircuit.GetState())
	}
	if !whitebirdCircuit.CanAttempt() {
		log.Printf("Health check: Whitebird circuit breaker is %s", whitebirdCircuit.GetState())
	}
}

func (ac *APICache) StopHealthMonitoring() {
	ac.healthStopOnce.Do(func() {
		close(ac.healthStopChan)
		if ac.healthTicker != nil {
			ac.healthTicker.Stop()
		}
	})
}
