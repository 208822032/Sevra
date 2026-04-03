package chat_context

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type HotPathStats struct {
	HotPathHit               uint64
	HotPathMiss              uint64
	MetaCacheHit             uint64
	MetaCacheMiss            uint64
	MetaCacheRefresh         uint64
	MetaCacheStale           uint64
	RecentWindowCacheHit     uint64
	RecentWindowCacheMiss    uint64
	RecentWindowCacheRefresh uint64
	RecentWindowCacheStale   uint64
}

type hotPathStats struct {
	HotPathHit               uint64
	HotPathMiss              uint64
	MetaCacheHit             uint64
	MetaCacheMiss            uint64
	MetaCacheRefresh         uint64
	MetaCacheStale           uint64
	RecentWindowCacheHit     uint64
	RecentWindowCacheMiss    uint64
	RecentWindowCacheRefresh uint64
	RecentWindowCacheStale   uint64
}

type sessionHotCache struct {
	mu        sync.Mutex
	sessions  map[string]*hotSessionState
	metaTTL   time.Duration
	windowTTL time.Duration
	hotTTL    time.Duration
	stats     hotPathStats
}

type hotSessionState struct {
	meta          *SessionMeta
	metaExpiresAt time.Time
	windows       map[string]*recentWindowCacheEntry
	lastWriteAt   time.Time
	lastWriteSeq  int64
	lastReadAt    time.Time
	lastWindowKey string
}

type recentWindowCacheEntry struct {
	afterSeq  int64
	limit     int
	lastSeq   int64
	turns     []*TurnRecord
	expiresAt time.Time
	updatedAt time.Time
}

func newSessionHotCache() *sessionHotCache {
	return &sessionHotCache{
		sessions:  make(map[string]*hotSessionState),
		metaTTL:   10 * time.Second,
		windowTTL: 10 * time.Second,
		hotTTL:    10 * time.Second,
	}
}

func (c *sessionHotCache) snapshot() HotPathStats {
	if c == nil {
		return HotPathStats{}
	}
	return HotPathStats{
		HotPathHit:               atomic.LoadUint64(&c.stats.HotPathHit),
		HotPathMiss:              atomic.LoadUint64(&c.stats.HotPathMiss),
		MetaCacheHit:             atomic.LoadUint64(&c.stats.MetaCacheHit),
		MetaCacheMiss:            atomic.LoadUint64(&c.stats.MetaCacheMiss),
		MetaCacheRefresh:         atomic.LoadUint64(&c.stats.MetaCacheRefresh),
		MetaCacheStale:           atomic.LoadUint64(&c.stats.MetaCacheStale),
		RecentWindowCacheHit:     atomic.LoadUint64(&c.stats.RecentWindowCacheHit),
		RecentWindowCacheMiss:    atomic.LoadUint64(&c.stats.RecentWindowCacheMiss),
		RecentWindowCacheRefresh: atomic.LoadUint64(&c.stats.RecentWindowCacheRefresh),
		RecentWindowCacheStale:   atomic.LoadUint64(&c.stats.RecentWindowCacheStale),
	}
}

func (c *sessionHotCache) getMeta(sessionID string) (*SessionMeta, bool, bool) {
	if c == nil || sessionID == "" {
		return nil, false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state(sessionID)
	if state == nil || state.meta == nil {
		atomic.AddUint64(&c.stats.MetaCacheMiss, 1)
		c.touchHotPathMiss()
		return nil, false, false
	}
	if !state.metaExpiresAt.IsZero() && time.Now().After(state.metaExpiresAt) {
		state.meta = nil
		state.metaExpiresAt = time.Time{}
		atomic.AddUint64(&c.stats.MetaCacheStale, 1)
		c.touchHotPathMiss()
		return nil, false, true
	}
	copyMeta := cloneSessionMeta(state.meta)
	atomic.AddUint64(&c.stats.MetaCacheHit, 1)
	c.touchHotPathHit()
	return copyMeta, true, false
}

func (c *sessionHotCache) putMeta(meta *SessionMeta) {
	if c == nil || meta == nil || meta.SessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state(meta.SessionID)
	if state == nil {
		return
	}
	state.meta = cloneSessionMeta(meta)
	if c.metaTTL > 0 {
		state.metaExpiresAt = time.Now().Add(c.metaTTL)
	}
	atomic.AddUint64(&c.stats.MetaCacheRefresh, 1)
}

func (c *sessionHotCache) getWindow(sessionID string, afterSeq int64, limit int, lastSeq int64) ([]*TurnRecord, bool, bool) {
	if c == nil || sessionID == "" || limit <= 0 {
		return nil, false, false
	}
	key := recentWindowCacheKey(afterSeq, limit)
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state(sessionID)
	if state == nil || state.windows == nil {
		atomic.AddUint64(&c.stats.RecentWindowCacheMiss, 1)
		c.touchHotPathMiss()
		return nil, false, false
	}
	entry, ok := state.windows[key]
	if !ok || entry == nil {
		atomic.AddUint64(&c.stats.RecentWindowCacheMiss, 1)
		state.lastWindowKey = key
		c.touchHotPathMiss()
		return nil, false, false
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		delete(state.windows, key)
		atomic.AddUint64(&c.stats.RecentWindowCacheStale, 1)
		state.lastWindowKey = key
		c.touchHotPathMiss()
		return nil, false, true
	}
	if entry.lastSeq != lastSeq {
		atomic.AddUint64(&c.stats.RecentWindowCacheStale, 1)
		state.lastWindowKey = key
		c.touchHotPathMiss()
		return nil, false, true
	}
	state.lastReadAt = time.Now()
	state.lastWindowKey = key
	atomic.AddUint64(&c.stats.RecentWindowCacheHit, 1)
	c.touchHotPathHit()
	turns := cloneTurns(entry.turns)
	return turns, true, false
}

func (c *sessionHotCache) putWindow(sessionID string, afterSeq int64, limit int, lastSeq int64, turns []*TurnRecord) {
	if c == nil || sessionID == "" || limit <= 0 {
		return
	}
	key := recentWindowCacheKey(afterSeq, limit)
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state(sessionID)
	if state == nil {
		return
	}
	if state.windows == nil {
		state.windows = make(map[string]*recentWindowCacheEntry)
	}
	state.windows[key] = &recentWindowCacheEntry{
		afterSeq:  afterSeq,
		limit:     limit,
		lastSeq:   lastSeq,
		turns:     cloneTurns(turns),
		expiresAt: time.Now().Add(c.windowTTL),
		updatedAt: time.Now(),
	}
	state.lastWindowKey = key
	atomic.AddUint64(&c.stats.RecentWindowCacheRefresh, 1)
}

func (c *sessionHotCache) updateAfterOpenTurn(turn *TurnRecord, meta *SessionMeta) {
	if c == nil || turn == nil || turn.SessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state(turn.SessionID)
	if state == nil {
		return
	}
	if meta != nil {
		state.meta = cloneSessionMeta(meta)
		if c.metaTTL > 0 {
			state.metaExpiresAt = time.Now().Add(c.metaTTL)
		}
		atomic.AddUint64(&c.stats.MetaCacheRefresh, 1)
	}
	state.lastWriteAt = time.Now()
	state.lastWriteSeq = turn.Seq
}

func (c *sessionHotCache) updateAfterCompleteTurn(turn *TurnRecord, meta *SessionMeta) {
	if c == nil || turn == nil || turn.SessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state(turn.SessionID)
	if state == nil {
		return
	}
	if meta != nil {
		state.meta = cloneSessionMeta(meta)
		if c.metaTTL > 0 {
			state.metaExpiresAt = time.Now().Add(c.metaTTL)
		}
		atomic.AddUint64(&c.stats.MetaCacheRefresh, 1)
	}
	state.lastWriteAt = time.Now()
	state.lastWriteSeq = turn.Seq
	if len(state.windows) > 0 {
		now := time.Now()
		for key, entry := range state.windows {
			if entry == nil {
				continue
			}
			if entry.lastSeq != turn.Seq-1 {
				continue
			}
			updated := append(cloneTurns(entry.turns), cloneTurnRecord(turn))
			if len(updated) > entry.limit {
				updated = updated[len(updated)-entry.limit:]
			}
			entry.turns = updated
			entry.lastSeq = turn.Seq
			entry.updatedAt = now
			if c.windowTTL > 0 {
				entry.expiresAt = now.Add(c.windowTTL)
			}
			newAfterSeq := turn.Seq - int64(entry.limit)
			if newAfterSeq < 0 {
				newAfterSeq = 0
			}
			newKey := recentWindowCacheKey(newAfterSeq, entry.limit)
			delete(state.windows, key)
			state.windows[newKey] = entry
			state.lastWindowKey = newKey
		}
	}
}

func (c *sessionHotCache) readLatencyBounds(sessionID string, cached bool) time.Duration {
	if c == nil || sessionID == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state(sessionID)
	if state == nil || state.lastWriteAt.IsZero() || !cached {
		return 0
	}
	return time.Since(state.lastWriteAt)
}

func (c *sessionHotCache) touchHotPathHit() {
	if c == nil {
		return
	}
	atomic.AddUint64(&c.stats.HotPathHit, 1)
}

func (c *sessionHotCache) touchHotPathMiss() {
	if c == nil {
		return
	}
	atomic.AddUint64(&c.stats.HotPathMiss, 1)
}

func (c *sessionHotCache) state(sessionID string) *hotSessionState {
	if c == nil || sessionID == "" {
		return nil
	}
	if c.sessions == nil {
		c.sessions = make(map[string]*hotSessionState)
	}
	state, ok := c.sessions[sessionID]
	if !ok {
		state = &hotSessionState{windows: make(map[string]*recentWindowCacheEntry)}
		c.sessions[sessionID] = state
	}
	return state
}

func recentWindowCacheKey(afterSeq int64, limit int) string {
	return strings.Join([]string{padSeq(afterSeq), padSeq(int64(limit))}, ":")
}
