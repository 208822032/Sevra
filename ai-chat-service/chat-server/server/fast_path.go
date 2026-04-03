package server

import (
	chat_context "ai-chat-service/chat-server/chat-context"
	"sync"
	"time"
)

type fastPathSessionState struct {
	meta                  *chat_context.SessionMeta
	metaExpiresAt         time.Time
	summary               *chat_context.SummaryRecord
	summaryExpiresAt      time.Time
	badSummaryHitStreak   int
	summaryCooldownUntil  time.Time
	lastPromptTokensSaved int
}

type sessionFastPathCache struct {
	mu         sync.Mutex
	entries    map[string]*fastPathSessionState
	metaTTL    time.Duration
	summaryTTL time.Duration
}

func newSessionFastPathCache() *sessionFastPathCache {
	return &sessionFastPathCache{
		entries:    make(map[string]*fastPathSessionState),
		metaTTL:    3 * time.Second,
		summaryTTL: 5 * time.Second,
	}
}

func (c *sessionFastPathCache) state(sessionID string) *fastPathSessionState {
	if c == nil || sessionID == "" {
		return nil
	}
	if c.entries == nil {
		c.entries = make(map[string]*fastPathSessionState)
	}
	state, ok := c.entries[sessionID]
	if !ok {
		state = &fastPathSessionState{}
		c.entries[sessionID] = state
	}
	return state
}

func (c *sessionFastPathCache) Meta(sessionID string) (*chat_context.SessionMeta, bool) {
	if c == nil || sessionID == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state(sessionID)
	if state == nil || state.meta == nil {
		return nil, false
	}
	if !state.metaExpiresAt.IsZero() && time.Now().After(state.metaExpiresAt) {
		state.meta = nil
		state.metaExpiresAt = time.Time{}
		return nil, false
	}
	copyMeta := *state.meta
	return &copyMeta, true
}

func (c *sessionFastPathCache) PutMeta(meta *chat_context.SessionMeta) {
	if c == nil || meta == nil || meta.SessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state(meta.SessionID)
	if state == nil {
		return
	}
	copyMeta := *meta
	state.meta = &copyMeta
	if c.metaTTL > 0 {
		state.metaExpiresAt = time.Now().Add(c.metaTTL)
	}
}

func (c *sessionFastPathCache) Summary(sessionID string) (*chat_context.SummaryRecord, bool) {
	if c == nil || sessionID == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state(sessionID)
	if state == nil || state.summary == nil {
		return nil, false
	}
	if !state.summaryExpiresAt.IsZero() && time.Now().After(state.summaryExpiresAt) {
		state.summary = nil
		state.summaryExpiresAt = time.Time{}
		return nil, false
	}
	copySummary := *state.summary
	return &copySummary, true
}

func (c *sessionFastPathCache) PutSummary(summary *chat_context.SummaryRecord) {
	if c == nil || summary == nil || summary.SessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state(summary.SessionID)
	if state == nil {
		return
	}
	copySummary := *summary
	state.summary = &copySummary
	if c.summaryTTL > 0 {
		state.summaryExpiresAt = time.Now().Add(c.summaryTTL)
	}
}

func (c *sessionFastPathCache) UpdateAfterOpenTurn(turn *chat_context.TurnRecord) {
	if c == nil || turn == nil || turn.SessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state(turn.SessionID)
	if state == nil {
		return
	}
	meta := state.ensureMeta(turn.SessionID)
	if meta.HeadSeq <= 0 || turn.Seq < meta.HeadSeq {
		meta.HeadSeq = turn.Seq
	}
	meta.LastSeq = turn.Seq
	meta.TurnCount = maxInt64Local(meta.TurnCount, turn.Seq)
	meta.UpdatedAtMS = turn.UpdatedAtMS
	meta.LastMessageID = turn.RequestMessageID
	meta.CompatMode = "session_seq"
	if c.metaTTL > 0 {
		state.metaExpiresAt = time.Now().Add(c.metaTTL)
	}
}

func (c *sessionFastPathCache) UpdateAfterCompleteTurn(turn *chat_context.TurnRecord, summary *chat_context.SummaryRecord) {
	if c == nil || turn == nil || turn.SessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state(turn.SessionID)
	if state == nil {
		return
	}
	meta := state.ensureMeta(turn.SessionID)
	if meta.HeadSeq <= 0 || turn.Seq < meta.HeadSeq {
		meta.HeadSeq = turn.Seq
	}
	if turn.Seq > meta.LastSeq {
		meta.LastSeq = turn.Seq
	}
	meta.TurnCount = maxInt64Local(meta.TurnCount, turn.Seq)
	meta.UpdatedAtMS = turn.UpdatedAtMS
	meta.LastMessageID = turn.AssistantMessageID
	meta.CompatMode = "session_seq"
	if c.metaTTL > 0 {
		state.metaExpiresAt = time.Now().Add(c.metaTTL)
	}
	if summary != nil {
		copySummary := *summary
		state.summary = &copySummary
		if c.summaryTTL > 0 {
			state.summaryExpiresAt = time.Now().Add(c.summaryTTL)
		}
	}
}

func (s *fastPathSessionState) ensureMeta(sessionID string) *chat_context.SessionMeta {
	if s.meta == nil {
		s.meta = &chat_context.SessionMeta{SessionID: sessionID}
	}
	return s.meta
}

func maxInt64Local(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
