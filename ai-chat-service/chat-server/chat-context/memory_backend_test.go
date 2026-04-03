package chat_context

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

type memoryRawBackend struct {
	mu   sync.Mutex
	data map[string]memoryEntry
	name string
}

type memoryEntry struct {
	value     string
	expiresAt time.Time
	hasTTL    bool
}

func newMemoryRawBackend(name string) *memoryRawBackend {
	return &memoryRawBackend{data: make(map[string]memoryEntry), name: name}
}

func (m *memoryRawBackend) Name() string { return m.name }

func (m *memoryRawBackend) Close() error { return nil }

func (m *memoryRawBackend) set(key, value string, ttl time.Duration) {
	entry := memoryEntry{value: value}
	if ttl > 0 {
		entry.hasTTL = true
		entry.expiresAt = time.Now().Add(ttl)
	}
	m.data[key] = entry
}

func (m *memoryRawBackend) get(key string) (string, error) {
	entry, ok := m.data[key]
	if !ok {
		return "", nil
	}
	if entry.hasTTL && time.Now().After(entry.expiresAt) {
		delete(m.data, key)
		return "", nil
	}
	return entry.value, nil
}

func (m *memoryRawBackend) RGet(ctx context.Context, key string) (string, error) { return m.get(key) }
func (m *memoryRawBackend) HGet(ctx context.Context, key string) (string, error) { return m.get(key) }
func (m *memoryRawBackend) RSet(ctx context.Context, key, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.set(key, value, ttl)
	return nil
}
func (m *memoryRawBackend) HSet(ctx context.Context, key, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.set(key, value, ttl)
	return nil
}
func (m *memoryRawBackend) TTL(ctx context.Context, key string) (time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.data[key]
	if !ok {
		return -2 * time.Second, nil
	}
	if !entry.hasTTL {
		return -1 * time.Second, nil
	}
	left := time.Until(entry.expiresAt)
	if left <= 0 {
		delete(m.data, key)
		return -2 * time.Second, nil
	}
	return left.Truncate(time.Second), nil
}

func (m *memoryRawBackend) Del(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memoryRawBackend) dump(key string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.data[key]
	if !ok {
		return "", false
	}
	if entry.hasTTL && time.Now().After(entry.expiresAt) {
		delete(m.data, key)
		return "", false
	}
	return entry.value, true
}

func (m *memoryRawBackend) rawJSON(key string, v any) {
	b, _ := json.Marshal(v)
	m.set(key, string(b), 0)
}

func (m *memoryRawBackend) mustInt(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func (m *memoryRawBackend) String() string {
	return fmt.Sprintf("memoryRawBackend(%s)", m.name)
}
