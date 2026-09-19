// Package cache is a byte-budgeted LRU cache of values of type V.
// It can be implemented in-memory or backed by a database.
package cache

import (
	"container/list"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"
)

// Cache is the interface for a byte-budgeted LRU cache.
type Cache[V any] interface {
	Get(key string) (V, bool)
	Put(key string, value V, size int64)
	Used() int64
	Len() int
	MaxBytes() int64
	ExpireAllForTesting()
}

// DefaultTTL is used by callers constructed without an explicit TTL.
const DefaultTTL = 5 * time.Minute

// memoryCache is an in-memory implementation of Cache.
type memoryCache[V any] struct {
	mu       sync.Mutex
	maxBytes int64
	ttl      time.Duration
	used     int64
	order    *list.List // keys, front = most recently used
	items    map[string]*entry[V]
}

type entry[V any] struct {
	value    V
	size     int64
	storedAt time.Time
	elem     *list.Element // LRU position; front = most recently used
}

// New returns a memory-based Cache.
func New[V any](maxBytes int64, ttl time.Duration) Cache[V] {
	return &memoryCache[V]{
		maxBytes: maxBytes,
		ttl:      ttl,
		order:    list.New(),
		items:    map[string]*entry[V]{},
	}
}

func (c *memoryCache[V]) Get(key string) (V, bool) {
	var zero V
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.items[key]
	if !ok {
		return zero, false
	}
	if c.ttl > 0 && time.Since(e.storedAt) > c.ttl {
		c.deleteLocked(key)
		return zero, false
	}
	c.order.MoveToFront(e.elem)
	return e.value, true
}

// Put stores value under key, evicting least-recently-used entries until the
// budget holds. An entry whose own size exceeds the whole budget is dropped.
func (c *memoryCache[V]) Put(key string, value V, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Replacing an existing key removes the old entry's size from the budget
	// before the new one is added, so a replacement never double-counts.
	if _, ok := c.items[key]; ok {
		c.deleteLocked(key)
	}
	e := &entry[V]{value: value, size: size, storedAt: time.Now()}
	e.elem = c.order.PushFront(key)
	c.items[key] = e
	c.used += size

	for c.used > c.maxBytes {
		back := c.order.Back()
		if back == nil {
			break
		}
		k, _ := back.Value.(string)
		if k == key {
			// This entry alone exceeds the budget: drop it rather than keep
			// an over-budget cache.
			c.deleteLocked(key)
			break
		}
		c.deleteLocked(k)
	}
}

// Used returns how many bytes of the budget are currently held, and Len how
// many entries; both are for observability (and tests).
func (c *memoryCache[V]) Used() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

func (c *memoryCache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// MaxBytes returns the cache's byte budget.
func (c *memoryCache[V]) MaxBytes() int64 {
	return c.maxBytes
}

// ExpireAllForTesting ages every entry past its TTL, so tests can exercise
// the expiry path without sleeping.
func (c *memoryCache[V]) ExpireAllForTesting() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.items {
		e.storedAt = time.Now().Add(-c.ttl - time.Second)
	}
}

// deleteLocked removes key; callers must hold c.mu.
func (c *memoryCache[V]) deleteLocked(key string) {
	e, ok := c.items[key]
	if !ok {
		return
	}
	delete(c.items, key)
	c.order.Remove(e.elem)
	c.used -= e.size
}

// databaseCache is a database-backed implementation of Cache.
type databaseCache[V any] struct {
	db       *gorm.DB
	maxBytes int64
	ttl      time.Duration
	table    string
}

type cacheRow struct {
	Key            string    `gorm:"primaryKey"`
	Value          []byte    `gorm:"type:blob"`
	Size           int64     `gorm:"index"`
	ExpiresAt      time.Time `gorm:"index"`
	LastAccessedAt time.Time `gorm:"index"`
}

// NewDatabase returns a database-backed Cache.
func NewDatabase[V any](db *gorm.DB, table string, maxBytes int64, ttl time.Duration) Cache[V] {
	if maxBytes <= 0 {
		maxBytes = 1 << 30 // Default 1GB if invalid
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	// Ensure table exists
	_ = db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		key TEXT PRIMARY KEY,
		value BLOB,
		size INTEGER,
		expires_at TIMESTAMP,
		last_accessed_at TIMESTAMP
	)`, table))

	return &databaseCache[V]{
		db:       db,
		maxBytes: maxBytes,
		ttl:      ttl,
		table:    table,
	}
}

func (c *databaseCache[V]) Get(key string) (V, bool) {
	var zero V
	var row cacheRow
	err := c.db.Table(c.table).
		Where("key = ? AND expires_at > ?", key, time.Now()).
		First(&row).Error
	if err != nil {
		return zero, false
	}

	// Update LRU
	_ = c.db.Table(c.table).Where("key = ?", key).Update("last_accessed_at", time.Now()).Error

	err = json.Unmarshal(row.Value, &zero)
	if err != nil {
		return zero, false
	}
	return zero, true
}

func (c *databaseCache[V]) Put(key string, value V, size int64) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}

	expiresAt := time.Now().Add(c.ttl)
	row := cacheRow{
		Key:            key,
		Value:          data,
		Size:          size,
		ExpiresAt:     expiresAt,
		LastAccessedAt: time.Now(),
	}

	// Upsert
	_ = c.db.Table(c.table).Save(&row).Error

	// Evict LRU
	c.evict()
}

func (c *databaseCache[V]) evict() {
	var currentUsed int64
	_ = c.db.Table(c.table).Select("SUM(size)").Scan(&currentUsed)

	if currentUsed > c.maxBytes {
		var oldest struct {
			Key string
		}
		// Get oldest item by access time
		_ = c.db.Table(c.table).Order("last_accessed_at asc").Select("key").Limit(1).Scan(&oldest)
		if oldest.Key != "" {
			_ = c.db.Table(c.table).Delete("key = ?", oldest.Key).Error
			c.evict() // Recursive eviction until budget is ok
		}
	}
}

func (c *databaseCache[V]) Used() int64 {
	var used int64
	_ = c.db.Table(c.table).Select("SUM(size)").Scan(&used)
	return used
}

func (c *databaseCache[V]) Len() int {
	var count int64
	_ = c.db.Table(c.table).Count(&count)
	return int(count)
}

func (c *databaseCache[V]) MaxBytes() int64 {
	return c.maxBytes
}

func (c *databaseCache[V]) ExpireAllForTesting() {
	_ = c.db.Table(c.table).Where("expires_at <= ?", time.Now()).Delete(nil)
}