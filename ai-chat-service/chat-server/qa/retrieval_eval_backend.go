package qa

import (
	"context"
	"strings"
	"sync"
	"time"
)

type evalMemoryKVLiteBackend struct {
	mu      sync.RWMutex
	values  map[string]string
	expires map[string]time.Time
	name    string
}

func newRetrievalEvalMemoryKVLiteBackend() *evalMemoryKVLiteBackend {
	return &evalMemoryKVLiteBackend{
		values:  make(map[string]string),
		expires: make(map[string]time.Time),
		name:    "retrieval-eval-memory-kvlite",
	}
}

func (b *evalMemoryKVLiteBackend) Name() string {
	if b == nil || strings.TrimSpace(b.name) == "" {
		return "retrieval-eval-memory-kvlite"
	}
	return b.name
}

func (b *evalMemoryKVLiteBackend) Close() error {
	return nil
}

func (b *evalMemoryKVLiteBackend) RGet(ctx context.Context, key string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if expiry, ok := b.expires[key]; ok && !expiry.IsZero() && time.Now().After(expiry) {
		delete(b.values, key)
		delete(b.expires, key)
		return "", nil
	}
	value, ok := b.values[key]
	if !ok {
		return "", nil
	}
	return value, nil
}

func (b *evalMemoryKVLiteBackend) RSet(ctx context.Context, key, value string, ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.values[key] = value
	if ttl > 0 {
		b.expires[key] = time.Now().Add(ttl)
	} else {
		delete(b.expires, key)
	}
	return nil
}

func (b *evalMemoryKVLiteBackend) Del(ctx context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.values, key)
	delete(b.expires, key)
	return nil
}
