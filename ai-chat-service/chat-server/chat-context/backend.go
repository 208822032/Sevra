package chat_context

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	metrics_bus "ai-chat-service/chat-server/metrics-bus"
	"ai-chat-service/pkg/config"
	predis "ai-chat-service/pkg/db/redis"

	"github.com/redis/go-redis/v9"
	"github.com/sashabaranov/go-openai"
)

type rawBackend interface {
	RGet(ctx context.Context, key string) (string, error)
	RSet(ctx context.Context, key, value string, ttl time.Duration) error
	HGet(ctx context.Context, key string) (string, error)
	HSet(ctx context.Context, key, value string, ttl time.Duration) error
	TTL(ctx context.Context, key string) (time.Duration, error)
	Del(ctx context.Context, key string) error
	Close() error
	Name() string
}

type kvliteBatchWriter interface {
	BatchWrite(ctx context.Context, commands [][]string) error
}

type SessionStore interface {
	ResolveSession(ctx context.Context, sessionID, requestMessageID, parentMessageID string) (string, error)
	LoadRecentTurns(ctx context.Context, sessionID string, limit int) ([]*TurnRecord, error)
	LoadTurnsSince(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]*TurnRecord, error)
	LoadSessionMeta(ctx context.Context, sessionID string) (*SessionMeta, error)
	LoadSummary(ctx context.Context, sessionID string) (*SummaryRecord, error)
	StoreSummary(ctx context.Context, summary *SummaryRecord, ttl time.Duration) error
	AllocateTurnSeq(ctx context.Context, sessionID string) (int64, error)
	WriteOpenTurn(ctx context.Context, turn *TurnRecord, ttl time.Duration) error
	CompleteTurn(ctx context.Context, sessionID string, seq int64, assistantMessageID string, assistantMessage openai.ChatCompletionMessage, assistantTokens int, ttl time.Duration) error
	Close() error
	Name() string
}

type sessionStore struct {
	raw       rawBackend
	namespace string
	metrics   *metrics_bus.BusMetrics
	hotCache  *sessionHotCache
}

func newSessionStore(raw rawBackend, namespace string, metrics ...*metrics_bus.BusMetrics) SessionStore {
	if namespace == "" {
		namespace = "ai:chat"
	}
	var metric *metrics_bus.BusMetrics
	if len(metrics) > 0 {
		metric = metrics[0]
	}
	return &sessionStore{raw: raw, namespace: namespace, metrics: metric, hotCache: newSessionHotCache()}
}

func (s *sessionStore) Name() string {
	if s == nil || s.raw == nil {
		return "unknown"
	}
	return s.raw.Name()
}

func (s *sessionStore) Close() error {
	if s == nil || s.raw == nil {
		return nil
	}
	return s.raw.Close()
}

func (s *sessionStore) hotPathStats() HotPathStats {
	if s == nil || s.hotCache == nil {
		return HotPathStats{}
	}
	return s.hotCache.snapshot()
}

func (s *sessionStore) ResolveSession(ctx context.Context, sessionID, requestMessageID, parentMessageID string) (string, error) {
	if s == nil || s.raw == nil {
		return "", errors.New("session store not initialized")
	}
	if sessionID != "" {
		return sessionID, nil
	}
	lookupKeys := []string{}
	if requestMessageID != "" {
		lookupKeys = append(lookupKeys, compatMessageKey(s.namespace, requestMessageID), compatParentKey(s.namespace, requestMessageID))
	}
	if parentMessageID != "" {
		lookupKeys = append(lookupKeys, compatMessageKey(s.namespace, parentMessageID), compatParentKey(s.namespace, parentMessageID))
	}
	for _, key := range lookupKeys {
		if sessionID, err := s.raw.HGet(ctx, key); err == nil && sessionID != "" {
			return sessionID, nil
		}
	}
	return newSessionID(), nil
}

func (s *sessionStore) LoadRecentTurns(ctx context.Context, sessionID string, limit int) ([]*TurnRecord, error) {
	if s == nil || s.raw == nil {
		return nil, errors.New("session store not initialized")
	}
	start := time.Now()
	turns, err := s.loadTurnsSinceCached(ctx, sessionID, 0, limit)
	if s.metrics != nil {
		s.metrics.ObserveLoadRecentTurnsLatency(time.Since(start))
	}
	return turns, err
}

func (s *sessionStore) LoadTurnsSince(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]*TurnRecord, error) {
	if s == nil || s.raw == nil {
		return nil, errors.New("session store not initialized")
	}
	start := time.Now()
	turns, err := s.loadTurnsSinceCached(ctx, sessionID, afterSeq, limit)
	if s.metrics != nil {
		s.metrics.ObserveHotSessionReadLatency(time.Since(start))
	}
	return turns, err
}

func (s *sessionStore) LoadSessionMeta(ctx context.Context, sessionID string) (*SessionMeta, error) {
	if s == nil || s.raw == nil {
		return nil, errors.New("session store not initialized")
	}
	start := time.Now()
	meta, hit, stale, err := s.loadMetaCached(ctx, sessionID)
	if s.metrics != nil {
		if hit {
			s.metrics.IncMetaCacheHitCount()
			s.metrics.IncFastPathHitCount()
		} else {
			s.metrics.IncMetaCacheMissCount()
			s.metrics.IncFastPathMissCount()
		}
		if stale {
			s.metrics.IncMetaCacheStaleCount()
		}
		s.metrics.ObserveLoadSessionMetaLatency(time.Since(start))
	}
	return meta, err
}

func (s *sessionStore) LoadSummary(ctx context.Context, sessionID string) (*SummaryRecord, error) {
	if s == nil || s.raw == nil {
		return nil, errors.New("session store not initialized")
	}
	if sessionID == "" {
		return nil, nil
	}
	str, err := s.raw.HGet(ctx, summaryKey(s.namespace, sessionID))
	if err != nil || strings.TrimSpace(str) == "" {
		return nil, err
	}
	summary := &SummaryRecord{}
	if err := json.Unmarshal([]byte(str), summary); err != nil {
		return nil, err
	}
	return summary, nil
}

func (s *sessionStore) loadMetaCached(ctx context.Context, sessionID string) (*SessionMeta, bool, bool, error) {
	if s == nil || s.raw == nil {
		return nil, false, false, errors.New("session store not initialized")
	}
	if sessionID == "" {
		return nil, false, false, nil
	}
	if s.hotCache != nil {
		if meta, ok, stale := s.hotCache.getMeta(sessionID); ok {
			return meta, true, stale, nil
		}
	}
	meta, err := s.loadMeta(ctx, sessionID)
	if err != nil {
		return nil, false, false, err
	}
	if s.hotCache != nil && meta != nil {
		s.hotCache.putMeta(meta)
		if s.metrics != nil {
			s.metrics.IncMetaCacheRefreshCount()
		}
	}
	return meta, false, false, nil
}

func (s *sessionStore) loadTurnsSinceCached(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]*TurnRecord, error) {
	if sessionID == "" || limit <= 0 {
		return []*TurnRecord{}, nil
	}
	meta, hit, stale, err := s.loadMetaCached(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if s.metrics != nil {
		if hit {
			s.metrics.IncMetaCacheHitCount()
		} else {
			s.metrics.IncMetaCacheMissCount()
			s.metrics.IncFastPathMissCount()
		}
		if stale {
			s.metrics.IncMetaCacheStaleCount()
		}
	}
	if meta == nil || meta.LastSeq <= 0 {
		return []*TurnRecord{}, nil
	}
	start := afterSeq + 1
	if start < 1 {
		start = 1
	}
	if remaining := meta.LastSeq - start + 1; remaining > int64(limit) {
		start = meta.LastSeq - int64(limit) + 1
		if start < 1 {
			start = 1
		}
	}
	if s.hotCache != nil {
		cachedTurns, hit, stale := s.hotCache.getWindow(sessionID, afterSeq, limit, meta.LastSeq)
		if hit {
			if s.metrics != nil {
				s.metrics.IncFastPathHitCount()
				s.metrics.IncRecentWindowCacheHitCount()
				s.metrics.ObserveHotSessionWriteThenReadLatency(s.hotCache.readLatencyBounds(sessionID, true))
			}
			return cachedTurns, nil
		}
		if s.metrics != nil {
			s.metrics.IncRecentWindowCacheMissCount()
		}
		if stale && s.metrics != nil {
			s.metrics.IncRecentWindowCacheStaleCount()
		}
	}
	turns, err := s.loadTurnsRange(ctx, sessionID, afterSeq, limit, meta)
	if err != nil {
		return nil, err
	}
	if s.hotCache != nil {
		s.hotCache.putWindow(sessionID, afterSeq, limit, meta.LastSeq, turns)
		if s.metrics != nil {
			s.metrics.IncRecentWindowCacheRefreshCount()
		}
	}
	return turns, nil
}

func (s *sessionStore) loadTurnsRange(ctx context.Context, sessionID string, afterSeq int64, limit int, meta *SessionMeta) ([]*TurnRecord, error) {
	if meta == nil || meta.LastSeq <= 0 {
		return []*TurnRecord{}, nil
	}
	start := afterSeq + 1
	if start < 1 {
		start = 1
	}
	if remaining := meta.LastSeq - start + 1; remaining > int64(limit) {
		start = meta.LastSeq - int64(limit) + 1
		if start < 1 {
			start = 1
		}
	}
	turns := make([]*TurnRecord, 0, limit)
	for seq := start; seq <= meta.LastSeq; seq++ {
		turn, err := s.loadTurn(ctx, sessionID, seq)
		if err != nil {
			return nil, err
		}
		if turn == nil || turn.TurnStatus != TurnStatusCompleted {
			continue
		}
		turns = append(turns, turn)
	}
	return turns, nil
}

func (s *sessionStore) StoreSummary(ctx context.Context, summary *SummaryRecord, ttl time.Duration) error {
	if s == nil || s.raw == nil {
		return errors.New("session store not initialized")
	}
	if summary == nil || summary.SessionID == "" {
		return errors.New("summary is nil")
	}
	now := nowMS()
	if summary.CreatedAtMS == 0 {
		summary.CreatedAtMS = now
	}
	summary.UpdatedAtMS = now
	if summary.SummarySeq == 0 {
		summary.SummarySeq = summary.SummarizedRangeEnd
	}
	if summary.SummarizedRangeEnd == 0 {
		summary.SummarizedRangeEnd = summary.SummarySeq
	}
	if summary.SummarizedRangeBegin == 0 {
		summary.SummarizedRangeBegin = summary.SummarySeq
	}
	if summary.ExpiresAtMS == 0 && ttl > 0 {
		summary.ExpiresAtMS = now + ttlToMS(int(ttl/time.Second))
	}
	bytes, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	if err := s.raw.HSet(ctx, summaryKey(s.namespace, summary.SessionID), string(bytes), ttl); err != nil {
		return err
	}
	meta, err := s.loadMeta(ctx, summary.SessionID)
	if err != nil {
		return err
	}
	if meta == nil {
		meta = &SessionMeta{SessionID: summary.SessionID}
	}
	meta.SessionID = summary.SessionID
	meta.SummarySeq = summary.SummarySeq
	meta.SummaryRef = summaryKey(s.namespace, summary.SessionID)
	meta.LastSeq = max64(meta.LastSeq, summary.SummarySeq)
	meta.UpdatedAtMS = now
	meta.normalize()
	if err := s.storeMeta(ctx, meta, ttl); err != nil {
		if repairErr := s.repairMeta(ctx, meta, ttl); repairErr != nil {
			return fmt.Errorf("store summary meta failed: %w; repair failed: %v", err, repairErr)
		}
	}
	return nil
}

func (s *sessionStore) AllocateTurnSeq(ctx context.Context, sessionID string) (int64, error) {
	meta, hit, _, err := s.loadMetaCached(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	if !hit {
		meta, err = s.loadMeta(ctx, sessionID)
		if err != nil {
			return 0, err
		}
	}
	if meta == nil || meta.LastSeq <= 0 {
		return 1, nil
	}
	return meta.LastSeq + 1, nil
}

func (s *sessionStore) WriteOpenTurn(ctx context.Context, turn *TurnRecord, ttl time.Duration) error {
	_, err := s.WriteOpenTurnWithMeta(ctx, turn, ttl)
	return err
}

func (s *sessionStore) WriteOpenTurnWithMeta(ctx context.Context, turn *TurnRecord, ttl time.Duration) (*SessionMeta, error) {
	if s == nil || s.raw == nil {
		return nil, errors.New("session store not initialized")
	}
	if turn == nil {
		return nil, errors.New("turn is nil")
	}
	if turn.SessionID == "" {
		return nil, errors.New("session_id is empty")
	}
	turn = cloneTurnRecord(turn)
	turn.TurnStatus = TurnStatusOpen
	now := nowMS()
	if turn.CreatedAtMS == 0 {
		turn.CreatedAtMS = now
	}
	turn.UpdatedAtMS = now
	turn.TotalTokens = turn.UserTokens
	turnBytes, err := json.Marshal(turn)
	if err != nil {
		return nil, err
	}
	meta, hit, _, err := s.loadMetaCached(ctx, turn.SessionID)
	if err != nil {
		return nil, err
	}
	if !hit {
		meta, err = s.loadMeta(ctx, turn.SessionID)
		if err != nil {
			return nil, err
		}
	}
	if meta == nil {
		meta = &SessionMeta{SessionID: turn.SessionID, HeadSeq: turn.Seq, LastSeq: turn.Seq, TurnCount: 1, UpdatedAtMS: now, TTLSeconds: int(ttl / time.Second), ExpiresAtMS: now + ttlToMS(int(ttl/time.Second)), CompatMode: "session_seq", LastMessageID: turn.RequestMessageID}
	} else {
		if meta.HeadSeq <= 0 || turn.Seq < meta.HeadSeq {
			meta.HeadSeq = turn.Seq
		}
		meta.LastSeq = turn.Seq
		meta.TurnCount++
		meta.UpdatedAtMS = now
		meta.TTLSeconds = int(ttl / time.Second)
		if ttl > 0 {
			meta.ExpiresAtMS = now + ttlToMS(int(ttl/time.Second))
		}
		meta.LastMessageID = turn.RequestMessageID
	}
	meta.SessionID = turn.SessionID
	meta.normalize()
	if bw, ok := s.raw.(kvliteBatchWriter); ok {
		if err := bw.BatchWrite(ctx, buildOpenTurnBatchCommands(s.namespace, turn, string(turnBytes), meta, ttl)); err != nil {
			return nil, err
		}
		if s.hotCache != nil {
			s.hotCache.updateAfterOpenTurn(turn, meta)
		}
		return meta, nil
	}
	if err := s.raw.RSet(ctx, turnKey(s.namespace, turn.SessionID, turn.Seq), string(turnBytes), ttl); err != nil {
		return nil, err
	}
	if err := s.storeMeta(ctx, meta, ttl); err != nil {
		if repairErr := s.repairMeta(ctx, meta, ttl); repairErr != nil {
			return nil, fmt.Errorf("store meta failed: %w; repair failed: %v", err, repairErr)
		}
		if s.metrics != nil {
			s.metrics.IncKvliteMetaRepairCount()
		}
	}
	if turn.RequestMessageID != "" {
		if err := s.raw.HSet(ctx, compatMessageKey(s.namespace, turn.RequestMessageID), turn.SessionID, ttl); err != nil {
			return nil, err
		}
	}
	if turn.ParentMessageID != "" {
		if err := s.raw.HSet(ctx, compatParentKey(s.namespace, turn.ParentMessageID), turn.SessionID, ttl); err != nil {
			return nil, err
		}
	}
	if s.hotCache != nil {
		s.hotCache.updateAfterOpenTurn(turn, meta)
	}
	return meta, nil
}

func (s *sessionStore) CompleteTurn(ctx context.Context, sessionID string, seq int64, assistantMessageID string, assistantMessage openai.ChatCompletionMessage, assistantTokens int, ttl time.Duration) error {
	turn, err := s.loadTurn(ctx, sessionID, seq)
	if err != nil {
		return err
	}
	if turn == nil {
		return errors.New("turn not found")
	}
	return s.CompleteTurnWithTurn(ctx, turn, nil, assistantMessageID, assistantMessage, assistantTokens, ttl)
}

func (s *sessionStore) CompleteTurnWithTurn(ctx context.Context, turn *TurnRecord, meta *SessionMeta, assistantMessageID string, assistantMessage openai.ChatCompletionMessage, assistantTokens int, ttl time.Duration) error {
	if s == nil || s.raw == nil {
		return errors.New("session store not initialized")
	}
	if turn == nil {
		return errors.New("turn is nil")
	}
	turn = cloneTurnRecord(turn)
	turn.AssistantMessage = assistantMessage
	turn.AssistantMessageID = assistantMessageID
	turn.AssistantTokens = assistantTokens
	turn.TotalTokens = turn.UserTokens + assistantTokens
	turn.TurnStatus = TurnStatusCompleted
	turn.UpdatedAtMS = nowMS()
	turnBytes, err := json.Marshal(turn)
	if err != nil {
		return err
	}
	if meta == nil {
		var hit bool
		meta, hit, _, err = s.loadMetaCached(ctx, turn.SessionID)
		if err != nil {
			return err
		}
		if !hit {
			meta, err = s.loadMeta(ctx, turn.SessionID)
			if err != nil {
				return err
			}
		}
	}
	if meta == nil {
		meta = &SessionMeta{SessionID: turn.SessionID, HeadSeq: turn.Seq, LastSeq: turn.Seq, TurnCount: 1, UpdatedAtMS: turn.UpdatedAtMS, TTLSeconds: int(ttl / time.Second), ExpiresAtMS: turn.UpdatedAtMS + ttlToMS(int(ttl/time.Second)), CompatMode: "session_seq", LastMessageID: assistantMessageID}
	} else {
		meta.LastSeq = turn.Seq
		if meta.HeadSeq <= 0 {
			meta.HeadSeq = turn.Seq
		}
		meta.UpdatedAtMS = turn.UpdatedAtMS
		meta.LastMessageID = assistantMessageID
		if ttl > 0 {
			meta.ExpiresAtMS = turn.UpdatedAtMS + ttlToMS(int(ttl/time.Second))
		}
	}
	meta.SessionID = turn.SessionID
	meta.normalize()
	if bw, ok := s.raw.(kvliteBatchWriter); ok {
		if err := bw.BatchWrite(ctx, buildCompleteTurnBatchCommands(s.namespace, turn, string(turnBytes), meta, ttl, assistantMessageID)); err != nil {
			return err
		}
		if s.hotCache != nil {
			s.hotCache.updateAfterCompleteTurn(turn, meta)
		}
		return nil
	}
	if err := s.raw.RSet(ctx, turnKey(s.namespace, turn.SessionID, turn.Seq), string(turnBytes), ttl); err != nil {
		return err
	}
	if err := s.storeMeta(ctx, meta, ttl); err != nil {
		if repairErr := s.repairMeta(ctx, meta, ttl); repairErr != nil {
			return fmt.Errorf("store meta failed: %w; repair failed: %v", err, repairErr)
		}
		if s.metrics != nil {
			s.metrics.IncKvliteMetaRepairCount()
		}
	}
	if assistantMessageID != "" {
		if err := s.raw.HSet(ctx, compatMessageKey(s.namespace, assistantMessageID), turn.SessionID, ttl); err != nil {
			return err
		}
	}
	if s.hotCache != nil {
		s.hotCache.updateAfterCompleteTurn(turn, meta)
	}
	return nil
}

func buildOpenTurnBatchCommands(namespace string, turn *TurnRecord, turnBytes string, meta *SessionMeta, ttl time.Duration) [][]string {
	commands := make([][]string, 0, 8)
	turnKeyName := turnKey(namespace, turn.SessionID, turn.Seq)
	metaKeyName := metaKey(namespace, turn.SessionID)
	metaBytes, _ := json.Marshal(meta)
	commands = append(commands, []string{"RSET", turnKeyName, turnBytes})
	if ttl > 0 {
		expireAt := strconv.FormatInt(time.Now().Add(ttl).UnixMilli(), 10)
		commands = append(commands, []string{"PEXPIREAT", turnKeyName, expireAt})
	}
	commands = append(commands, []string{"HSET", metaKeyName, string(metaBytes)})
	if ttl > 0 {
		expireAt := strconv.FormatInt(time.Now().Add(ttl).UnixMilli(), 10)
		commands = append(commands, []string{"PEXPIREAT", metaKeyName, expireAt})
	}
	if turn.RequestMessageID != "" {
		compatKey := compatMessageKey(namespace, turn.RequestMessageID)
		commands = append(commands, []string{"HSET", compatKey, turn.SessionID})
		if ttl > 0 {
			expireAt := strconv.FormatInt(time.Now().Add(ttl).UnixMilli(), 10)
			commands = append(commands, []string{"PEXPIREAT", compatKey, expireAt})
		}
	}
	if turn.ParentMessageID != "" {
		compatKey := compatParentKey(namespace, turn.ParentMessageID)
		commands = append(commands, []string{"HSET", compatKey, turn.SessionID})
		if ttl > 0 {
			expireAt := strconv.FormatInt(time.Now().Add(ttl).UnixMilli(), 10)
			commands = append(commands, []string{"PEXPIREAT", compatKey, expireAt})
		}
	}
	return commands
}

func buildCompleteTurnBatchCommands(namespace string, turn *TurnRecord, turnBytes string, meta *SessionMeta, ttl time.Duration, assistantMessageID string) [][]string {
	commands := make([][]string, 0, 6)
	turnKeyName := turnKey(namespace, turn.SessionID, turn.Seq)
	metaKeyName := metaKey(namespace, turn.SessionID)
	metaBytes, _ := json.Marshal(meta)
	commands = append(commands, []string{"RSET", turnKeyName, turnBytes})
	if ttl > 0 {
		expireAt := strconv.FormatInt(time.Now().Add(ttl).UnixMilli(), 10)
		commands = append(commands, []string{"PEXPIREAT", turnKeyName, expireAt})
	}
	commands = append(commands, []string{"HSET", metaKeyName, string(metaBytes)})
	if ttl > 0 {
		expireAt := strconv.FormatInt(time.Now().Add(ttl).UnixMilli(), 10)
		commands = append(commands, []string{"PEXPIREAT", metaKeyName, expireAt})
	}
	if assistantMessageID != "" {
		compatKey := compatMessageKey(namespace, assistantMessageID)
		commands = append(commands, []string{"HSET", compatKey, turn.SessionID})
		if ttl > 0 {
			expireAt := strconv.FormatInt(time.Now().Add(ttl).UnixMilli(), 10)
			commands = append(commands, []string{"PEXPIREAT", compatKey, expireAt})
		}
	}
	return commands
}

func (s *sessionStore) loadTurn(ctx context.Context, sessionID string, seq int64) (*TurnRecord, error) {
	str, err := s.raw.RGet(ctx, turnKey(s.namespace, sessionID, seq))
	if err != nil || strings.TrimSpace(str) == "" {
		return nil, err
	}
	turn := &TurnRecord{}
	if err := json.Unmarshal([]byte(str), turn); err != nil {
		return nil, err
	}
	return turn, nil
}

func (s *sessionStore) loadMeta(ctx context.Context, sessionID string) (*SessionMeta, error) {
	str, err := s.raw.HGet(ctx, metaKey(s.namespace, sessionID))
	if err != nil || strings.TrimSpace(str) == "" {
		return nil, err
	}
	meta := &SessionMeta{}
	if err := json.Unmarshal([]byte(str), meta); err != nil {
		return nil, err
	}
	meta.normalize()
	return meta, nil
}

func (s *sessionStore) storeMeta(ctx context.Context, meta *SessionMeta, ttl time.Duration) error {
	if meta == nil {
		return errors.New("meta is nil")
	}
	bytes, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return s.raw.HSet(ctx, metaKey(s.namespace, meta.SessionID), string(bytes), ttl)
}

func (s *sessionStore) repairMeta(ctx context.Context, meta *SessionMeta, ttl time.Duration) error {
	if meta == nil {
		return errors.New("meta is nil")
	}
	if err := s.storeMeta(ctx, meta, ttl); err != nil {
		return err
	}
	return nil
}

type redisRawBackend struct {
	client    *redis.Client
	namespace string
}

func newRedisRawBackend(cnf *config.Config, namespace string) rawBackend {
	pool := predis.GetPool()
	client := pool.Get()
	if namespace == "" {
		namespace = "ai:chat"
	}
	return &redisRawBackend{client: client, namespace: namespace}
}

func (r *redisRawBackend) Name() string { return "redis" }

func (r *redisRawBackend) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	predis.GetPool().Put(r.client)
	return nil
}

func (r *redisRawBackend) RGet(ctx context.Context, key string) (string, error) {
	val, err := r.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return val, err
}

func (r *redisRawBackend) RSet(ctx context.Context, key, value string, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}

func (r *redisRawBackend) HGet(ctx context.Context, key string) (string, error) {
	val, err := r.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return val, err
}

func (r *redisRawBackend) HSet(ctx context.Context, key, value string, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}

func (r *redisRawBackend) TTL(ctx context.Context, key string) (time.Duration, error) {
	return r.client.TTL(ctx, key).Result()
}

func (r *redisRawBackend) Del(ctx context.Context, key string) error {
	return r.client.Del(ctx, key).Err()
}

type kvliteRawBackend struct {
	addr           string
	password       string
	namespace      string
	connectTimeout time.Duration
	readTimeout    time.Duration
	writeTimeout   time.Duration
	maxRetries     int
	retryBackoff   time.Duration
	pool           *kvliteConnPool
	metrics        *metrics_bus.BusMetrics
}

func newKVLiteRawBackend(cnf *config.Config, namespace string, metrics *metrics_bus.BusMetrics) rawBackend {
	if namespace == "" {
		namespace = "ai:chat"
	}
	ctxCfg := cnf.Context.KVLite
	defaultTimeout := time.Duration(ctxCfg.TimeoutMS) * time.Millisecond
	if defaultTimeout <= 0 {
		defaultTimeout = 2 * time.Second
	}
	connectTimeout := time.Duration(ctxCfg.ConnectTimeoutMS) * time.Millisecond
	if connectTimeout <= 0 {
		connectTimeout = defaultTimeout
	}
	readTimeout := time.Duration(ctxCfg.ReadTimeoutMS) * time.Millisecond
	if readTimeout <= 0 {
		readTimeout = defaultTimeout
	}
	writeTimeout := time.Duration(ctxCfg.WriteTimeoutMS) * time.Millisecond
	if writeTimeout <= 0 {
		writeTimeout = defaultTimeout
	}
	poolSize := ctxCfg.PoolSize
	if poolSize <= 0 {
		poolSize = 4
	}
	maxIdle := ctxCfg.MaxIdle
	if maxIdle <= 0 || maxIdle > poolSize {
		maxIdle = poolSize
	}
	maxRetries := ctxCfg.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	retryBackoff := time.Duration(ctxCfg.RetryBackoffMS) * time.Millisecond
	if retryBackoff <= 0 {
		retryBackoff = 50 * time.Millisecond
	}
	backend := &kvliteRawBackend{
		addr:           ctxCfg.Addr,
		password:       ctxCfg.Password,
		namespace:      namespace,
		connectTimeout: connectTimeout,
		readTimeout:    readTimeout,
		writeTimeout:   writeTimeout,
		maxRetries:     maxRetries,
		retryBackoff:   retryBackoff,
		metrics:        metrics,
	}
	backend.pool = newKVLiteConnPool(ctxCfg.Addr, poolSize, maxIdle, connectTimeout)
	return backend
}

func (k *kvliteRawBackend) Name() string { return "kvlite" }

func (k *kvliteRawBackend) Close() error {
	if k == nil || k.pool == nil {
		return nil
	}
	k.pool.Close()
	return nil
}

func (k *kvliteRawBackend) do(ctx context.Context, op string, retryable bool, exec func(*kvliteConn) (any, error)) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	attempts := 0
	for {
		start := time.Now()
		conn, err := k.pool.Acquire(ctx)
		if err != nil {
			k.recordError(op, err)
			return nil, k.wrapError(op, err)
		}
		val, execErr := exec(conn)
		if execErr != nil {
			k.pool.Release(conn, false)
			k.recordError(op, execErr)
			if retryable && attempts < k.maxRetries && isRetryableBackendError(execErr) {
				attempts++
				time.Sleep(k.retryBackoff)
				continue
			}
			return nil, k.wrapError(op, execErr)
		}
		k.pool.Release(conn, true)
		k.recordLatency(op, time.Since(start))
		return val, nil
	}
}

func (k *kvliteRawBackend) doBatch(ctx context.Context, op string, retryable bool, commands [][]string) ([]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	attempts := 0
	for {
		start := time.Now()
		conn, err := k.pool.Acquire(ctx)
		if err != nil {
			k.recordError(op, err)
			return nil, k.wrapError(op, err)
		}
		vals, execErr := conn.execBatch(k.writeTimeout, k.readTimeout, commands)
		if execErr != nil {
			k.pool.Release(conn, false)
			k.recordError(op, execErr)
			if retryable && attempts < k.maxRetries && isRetryableBackendError(execErr) {
				attempts++
				time.Sleep(k.retryBackoff)
				continue
			}
			return nil, k.wrapError(op, execErr)
		}
		k.pool.Release(conn, true)
		k.recordLatency(op, time.Since(start))
		return vals, nil
	}
}

func (k *kvliteRawBackend) RGet(ctx context.Context, key string) (string, error) {
	val, err := k.do(ctx, "RGET", true, func(conn *kvliteConn) (any, error) {
		return conn.exec(k.writeTimeout, k.readTimeout, "RGET", key)
	})
	if err != nil {
		return "", err
	}
	str, err := respStringValue(val)
	if err != nil {
		return "", err
	}
	if isNotFoundPayload(str) {
		return "", nil
	}
	return str, nil
}

func (k *kvliteRawBackend) RSet(ctx context.Context, key, value string, ttl time.Duration) error {
	commands := [][]string{{"RSET", key, value}}
	if ttl > 0 {
		commands = append(commands, []string{"PEXPIREAT", key, strconv.FormatInt(time.Now().Add(ttl).UnixMilli(), 10)})
	}
	_, err := k.doBatch(ctx, "RSET", true, commands)
	return err
}

func (k *kvliteRawBackend) HGet(ctx context.Context, key string) (string, error) {
	val, err := k.do(ctx, "HGET", true, func(conn *kvliteConn) (any, error) {
		return conn.exec(k.writeTimeout, k.readTimeout, "HGET", key)
	})
	if err != nil {
		return "", err
	}
	str, err := respStringValue(val)
	if err != nil {
		return "", err
	}
	if isNotFoundPayload(str) {
		return "", nil
	}
	return str, nil
}

func (k *kvliteRawBackend) HSet(ctx context.Context, key, value string, ttl time.Duration) error {
	commands := [][]string{{"HSET", key, value}}
	if ttl > 0 {
		commands = append(commands, []string{"PEXPIREAT", key, strconv.FormatInt(time.Now().Add(ttl).UnixMilli(), 10)})
	}
	_, err := k.doBatch(ctx, "HSET", true, commands)
	return err
}

func (k *kvliteRawBackend) BatchWrite(ctx context.Context, commands [][]string) error {
	_, err := k.doBatch(ctx, "BATCH", true, commands)
	return err
}

func (k *kvliteRawBackend) TTL(ctx context.Context, key string) (time.Duration, error) {
	val, err := k.do(ctx, "TTL", true, func(conn *kvliteConn) (any, error) {
		return conn.exec(k.writeTimeout, k.readTimeout, "TTL", key)
	})
	if err != nil {
		return 0, err
	}
	switch v := val.(type) {
	case int64:
		return time.Duration(v) * time.Second, nil
	case string:
		n, convErr := strconv.ParseInt(v, 10, 64)
		if convErr != nil {
			return 0, convErr
		}
		return time.Duration(n) * time.Second, nil
	default:
		return 0, fmt.Errorf("unexpected ttl response %T", val)
	}
}

func (k *kvliteRawBackend) Del(ctx context.Context, key string) error {
	if _, err := k.do(ctx, "DEL", true, func(conn *kvliteConn) (any, error) {
		return conn.exec(k.writeTimeout, k.readTimeout, "DEL", key)
	}); err != nil {
		return err
	}
	return nil
}

func (k *kvliteRawBackend) recordLatency(op string, d time.Duration) {
	if k == nil || k.metrics == nil {
		return
	}
	if op == "RGET" || op == "HGET" || op == "TTL" {
		k.metrics.ObserveKvliteReadLatency(d)
		return
	}
	k.metrics.ObserveKvliteWriteLatency(d)
}

func (k *kvliteRawBackend) recordError(op string, err error) {
	if k == nil || k.metrics == nil || err == nil {
		return
	}
	if errors.Is(err, ErrKvliteTimeout) {
		k.metrics.IncKvliteTimeoutCount()
		return
	}
	if errors.Is(err, ErrKvliteConnError) {
		k.metrics.IncKvliteConnectionErrorCount()
	}
}

func (k *kvliteRawBackend) wrapError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrKvliteTimeout) || isTimeoutError(err) {
		return fmt.Errorf("%w: %s %v", ErrKvliteTimeout, op, err)
	}
	if errors.Is(err, ErrKvliteConnError) || isConnError(err) {
		return fmt.Errorf("%w: %s %v", ErrKvliteConnError, op, err)
	}
	return fmt.Errorf("%s failed: %w", op, err)
}

func isRetryableBackendError(err error) bool {
	return errors.Is(err, ErrKvliteTimeout) || errors.Is(err, ErrKvliteConnError) || isTimeoutError(err) || isConnError(err)
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "timeout")
}

func isConnError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused") || strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset") || strings.Contains(msg, "use of closed network connection")
}

var (
	ErrKvliteTimeout   = errors.New("kvlite backend timeout")
	ErrKvliteConnError = errors.New("kvlite backend connection error")
)

type kvliteConn struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
}

func (c *kvliteConn) close() {
	if c == nil || c.conn == nil {
		return
	}
	_ = c.conn.Close()
	c.conn = nil
	c.r = nil
	c.w = nil
}

func (c *kvliteConn) exec(writeTimeout, readTimeout time.Duration, args ...string) (any, error) {
	if c == nil || c.conn == nil {
		return nil, ErrKvliteConnError
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return nil, err
	}
	if err := writeRESPArray(c.w, args); err != nil {
		return nil, err
	}
	if err := c.w.Flush(); err != nil {
		return nil, err
	}
	if err := c.conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return nil, err
	}
	val, err := readRESPValue(c.r)
	if err != nil {
		return nil, err
	}
	return val, nil
}

func (c *kvliteConn) execBatch(writeTimeout, readTimeout time.Duration, commands [][]string) ([]any, error) {
	if c == nil || c.conn == nil {
		return nil, ErrKvliteConnError
	}
	if len(commands) == 0 {
		return nil, nil
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return nil, err
	}
	for _, args := range commands {
		if err := writeRESPArray(c.w, args); err != nil {
			return nil, err
		}
	}
	if err := c.w.Flush(); err != nil {
		return nil, err
	}
	if err := c.conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return nil, err
	}
	vals := make([]any, 0, len(commands))
	for range commands {
		val, err := readRESPValue(c.r)
		if err != nil {
			return nil, err
		}
		vals = append(vals, val)
	}
	return vals, nil
}

type kvliteConnPool struct {
	addr           string
	maxSize        int
	maxIdle        int
	connectTimeout time.Duration
	mu             sync.Mutex
	active         int
	idle           chan *kvliteConn
	closed         bool
}

func newKVLiteConnPool(addr string, maxSize, maxIdle int, connectTimeout time.Duration) *kvliteConnPool {
	if maxSize <= 0 {
		maxSize = 1
	}
	if maxIdle <= 0 || maxIdle > maxSize {
		maxIdle = maxSize
	}
	if connectTimeout <= 0 {
		connectTimeout = 2 * time.Second
	}
	return &kvliteConnPool{
		addr:           addr,
		maxSize:        maxSize,
		maxIdle:        maxIdle,
		connectTimeout: connectTimeout,
		idle:           make(chan *kvliteConn, maxIdle),
	}
}

func (p *kvliteConnPool) Acquire(ctx context.Context) (*kvliteConn, error) {
	if p == nil {
		return nil, ErrKvliteConnError
	}
	select {
	case conn := <-p.idle:
		if conn != nil && conn.conn != nil {
			return conn, nil
		}
	default:
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrKvliteConnError
	}
	if p.active < p.maxSize {
		p.active++
		p.mu.Unlock()
		conn, err := p.dial()
		if err != nil {
			p.mu.Lock()
			p.active--
			p.mu.Unlock()
			return nil, err
		}
		return conn, nil
	}
	idle := p.idle
	p.mu.Unlock()
	waitTimer := time.NewTimer(p.connectTimeout)
	defer waitTimer.Stop()
	select {
	case conn := <-idle:
		if conn != nil && conn.conn != nil {
			return conn, nil
		}
		return nil, ErrKvliteConnError
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-waitTimer.C:
		return nil, ErrKvliteTimeout
	}
}

func (p *kvliteConnPool) Release(conn *kvliteConn, keep bool) {
	if p == nil || conn == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !keep || conn.conn == nil {
		conn.close()
		if p.active > 0 {
			p.active--
		}
		return
	}
	select {
	case p.idle <- conn:
	default:
		conn.close()
		if p.active > 0 {
			p.active--
		}
	}
}

func (p *kvliteConnPool) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()
	if idle == nil {
		return
	}
	for {
		select {
		case conn, ok := <-idle:
			if !ok {
				return
			}
			if conn != nil {
				conn.close()
			}
		default:
			return
		}
	}
}

func (p *kvliteConnPool) dial() (*kvliteConn, error) {
	conn, err := net.DialTimeout("tcp", p.addr, p.connectTimeout)
	if err != nil {
		if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
			return nil, fmt.Errorf("%w: %v", ErrKvliteTimeout, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrKvliteConnError, err)
	}
	return &kvliteConn{conn: conn, r: bufio.NewReader(conn), w: bufio.NewWriter(conn)}, nil
}

func writeRESPArray(w *bufio.Writer, args []string) error {
	if _, err := fmt.Fprintf(w, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(w, "$%d\r\n%s\r\n", len(arg), arg); err != nil {
			return err
		}
	}
	return nil
}

func readRESPValue(r *bufio.Reader) (any, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	switch prefix {
	case '+':
		return line, nil
	case '-':
		if strings.Contains(strings.ToUpper(line), "NO EXIST") || strings.Contains(strings.ToLower(line), "nil") {
			return nil, nil
		}
		return nil, errors.New(line)
	case ':':
		return strconv.ParseInt(line, 10, 64)
	case '$':
		n, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if n == -1 {
			return nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		return nil, errors.New("array response not supported")
	default:
		return strings.TrimSpace(string(prefix) + line), nil
	}
}

func isNotFoundPayload(s string) bool {
	s = strings.TrimSpace(strings.ToUpper(s))
	return s == "NO EXIST" || s == "NIL" || s == "(NIL)"
}

func respStringValue(v any) (string, error) {
	switch val := v.(type) {
	case nil:
		return "", nil
	case string:
		return val, nil
	case []byte:
		return string(val), nil
	case int64:
		return strconv.FormatInt(val, 10), nil
	default:
		return "", fmt.Errorf("unexpected resp value type %T", v)
	}
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
