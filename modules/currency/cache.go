package currency

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	calculationCacheTTL  = 5 * time.Minute
	cacheCleanupInterval = 5 * time.Minute
)

type ConversionCache struct {
	mu             sync.RWMutex
	results        map[string]*CachedConversion
	lastCleanup    time.Time
	cleanupRunning int32
}

type CachedConversion struct {
	value     float64
	timestamp time.Time
}

var globalConversionCache = &ConversionCache{
	results:     make(map[string]*CachedConversion),
	lastCleanup: time.Now(),
}

func (c *ConversionCache) Get(key string) (float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if result, ok := c.results[key]; ok {
		if time.Since(result.timestamp) < calculationCacheTTL {
			return result.value, true
		}
	}
	return 0, false
}

func (c *ConversionCache) Set(key string, value float64) {
	if !isValidFloat(value) {
		return
	}

	c.mu.Lock()
	c.results[key] = &CachedConversion{
		value:     value,
		timestamp: time.Now(),
	}
	c.mu.Unlock()

	if time.Since(c.lastCleanup) > cacheCleanupInterval {
		c.scheduleCleanup()
	}
}

func (c *ConversionCache) scheduleCleanup() {
	if !atomic.CompareAndSwapInt32(&c.cleanupRunning, 0, 1) {
		return
	}

	go func() {
		defer atomic.StoreInt32(&c.cleanupRunning, 0)
		time.Sleep(100 * time.Millisecond)

		c.mu.Lock()
		defer c.mu.Unlock()

		now := time.Now()
		for k, v := range c.results {
			if now.Sub(v.timestamp) > calculationCacheTTL*2 {
				delete(c.results, k)
			}
		}
		c.lastCleanup = now
	}()
}

// FormatCacheKey creates bucketed cache keys
func formatCacheKey(from, to string, amount float64) string {
	from = strings.ToUpper(strings.TrimSpace(from))
	to = strings.ToUpper(strings.TrimSpace(to))

	var bucket string
	switch {
	case amount < 10:
		bucket = "S"
	case amount < 100:
		bucket = "M"
	case amount < 1000:
		bucket = "L"
	case amount < 10000:
		bucket = "XL"
	default:
		bucket = "XXL"
	}

	return fmt.Sprintf("%s_%s_%s", from, to, bucket)
}
