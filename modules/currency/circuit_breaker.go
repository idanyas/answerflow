package currency

import (
	"sync"
	"time"
)

const (
	circuitBreakerThreshold   = 5
	circuitBreakerTimeout     = 5 * time.Minute
	circuitBreakerHalfOpenMax = 3
)

type CircuitBreaker struct {
	mu                 sync.RWMutex
	failures           int
	consecutiveSuccess int
	lastFailTime       time.Time
	state              string
	openUntil          time.Time
	halfOpenAttempts   int
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.consecutiveSuccess = 0
	cb.lastFailTime = time.Now()

	if cb.state == "half-open" {
		cb.state = "open"
		cb.openUntil = time.Now().Add(circuitBreakerTimeout)
		cb.halfOpenAttempts = 0
	} else if cb.failures >= circuitBreakerThreshold {
		cb.state = "open"
		cb.openUntil = time.Now().Add(circuitBreakerTimeout)
	}
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.consecutiveSuccess++

	switch cb.state {
	case "half-open":
		if cb.consecutiveSuccess >= 2 {
			cb.state = "closed"
			cb.failures = 0
			cb.halfOpenAttempts = 0
		}
	case "closed":
		if cb.consecutiveSuccess >= 3 {
			cb.failures = 0
		}
	}
}

func (cb *CircuitBreaker) CanAttempt() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case "closed":
		return true
	case "half-open":
		if cb.halfOpenAttempts < circuitBreakerHalfOpenMax {
			cb.halfOpenAttempts++
			return true
		}
		return false
	case "open":
		if time.Now().After(cb.openUntil) {
			cb.state = "half-open"
			cb.halfOpenAttempts = 1
			cb.consecutiveSuccess = 0
			return true
		}
		return false
	default:
		cb.state = "closed"
		return true
	}
}

var (
	whitebirdCircuit  = &CircuitBreaker{state: "closed"}
	bybitCircuit      = &CircuitBreaker{state: "closed"}
	mastercardCircuit = &CircuitBreaker{state: "closed"}
)
