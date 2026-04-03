package chat_context

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	metrics_bus "ai-chat-service/chat-server/metrics-bus"
	"ai-chat-service/pkg/config"
	"ai-chat-service/pkg/log"

	"github.com/sashabaranov/go-openai"
)

type compositeStore struct {
	primary          SessionStore
	fallback         SessionStore
	dualWrite        bool
	fallbackEnable   bool
	shadowRead       bool
	compareEnabled   bool
	repairOnMismatch bool
	contextTTL       time.Duration
	summaryTTL       time.Duration
	compareTimeout   time.Duration
	metrics          *metrics_bus.BusMetrics
	logger           log.ILogger
	name             string
}

type metaWriter interface {
	storeMeta(ctx context.Context, meta *SessionMeta, ttl time.Duration) error
}

type openTurnWriter interface {
	WriteOpenTurnWithMeta(ctx context.Context, turn *TurnRecord, ttl time.Duration) (*SessionMeta, error)
}

type completeTurnWriter interface {
	CompleteTurnWithTurn(ctx context.Context, turn *TurnRecord, meta *SessionMeta, assistantMessageID string, assistantMessage openai.ChatCompletionMessage, assistantTokens int, ttl time.Duration) error
}

func (c *compositeStore) Name() string {
	if c == nil || c.primary == nil {
		return "unknown"
	}
	return c.name
}

func (c *compositeStore) Close() error {
	if c == nil {
		return nil
	}
	if c.fallback != nil {
		_ = c.fallback.Close()
	}
	if c.primary != nil {
		return c.primary.Close()
	}
	return nil
}

func (c *compositeStore) ResolveSession(ctx context.Context, sessionID, requestMessageID, parentMessageID string) (string, error) {
	if c == nil || c.primary == nil {
		return "", errors.New("session store not initialized")
	}
	sessionID, err := c.primary.ResolveSession(ctx, sessionID, requestMessageID, parentMessageID)
	if err == nil && sessionID != "" {
		return sessionID, nil
	}
	if c.fallbackEnable && c.fallback != nil {
		if errors.Is(err, ErrKvliteTimeout) || errors.Is(err, ErrKvliteConnError) {
			if c.metrics != nil {
				c.metrics.IncKvliteFallbackCount()
			}
			return c.fallback.ResolveSession(ctx, sessionID, requestMessageID, parentMessageID)
		}
	}
	if err != nil {
		return "", err
	}
	return "", nil
}

func (c *compositeStore) LoadRecentTurns(ctx context.Context, sessionID string, limit int) ([]*TurnRecord, error) {
	return c.LoadTurnsSince(ctx, sessionID, 0, limit)
}

func (c *compositeStore) LoadTurnsSince(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]*TurnRecord, error) {
	if c == nil || c.primary == nil {
		return nil, errors.New("session store not initialized")
	}
	turns, err := c.primary.LoadTurnsSince(ctx, sessionID, afterSeq, limit)
	if err == nil {
		return turns, nil
	}
	if c.fallbackEnable && c.fallback != nil {
		if errors.Is(err, ErrKvliteTimeout) || errors.Is(err, ErrKvliteConnError) {
			if c.metrics != nil {
				c.metrics.IncKvliteFallbackCount()
			}
			return c.fallback.LoadTurnsSince(ctx, sessionID, afterSeq, limit)
		}
	}
	return turns, err
}

func (c *compositeStore) LoadSessionMeta(ctx context.Context, sessionID string) (*SessionMeta, error) {
	if c == nil || c.primary == nil {
		return nil, errors.New("session store not initialized")
	}
	meta, err := c.primary.LoadSessionMeta(ctx, sessionID)
	if err == nil {
		return meta, nil
	}
	if c.fallbackEnable && c.fallback != nil {
		if errors.Is(err, ErrKvliteTimeout) || errors.Is(err, ErrKvliteConnError) {
			if c.metrics != nil {
				c.metrics.IncKvliteFallbackCount()
			}
			return c.fallback.LoadSessionMeta(ctx, sessionID)
		}
	}
	return meta, err
}

func (c *compositeStore) LoadSummary(ctx context.Context, sessionID string) (*SummaryRecord, error) {
	if c == nil || c.primary == nil {
		return nil, errors.New("session store not initialized")
	}
	summary, err := c.primary.LoadSummary(ctx, sessionID)
	if err == nil {
		return summary, nil
	}
	if c.fallbackEnable && c.fallback != nil {
		if errors.Is(err, ErrKvliteTimeout) || errors.Is(err, ErrKvliteConnError) {
			if c.metrics != nil {
				c.metrics.IncKvliteFallbackCount()
			}
			return c.fallback.LoadSummary(ctx, sessionID)
		}
	}
	return summary, err
}

func (c *compositeStore) StoreSummary(ctx context.Context, summary *SummaryRecord, ttl time.Duration) error {
	if c == nil || c.primary == nil {
		return errors.New("session store not initialized")
	}
	if err := c.primary.StoreSummary(ctx, summary, ttl); err != nil {
		if c.fallbackEnable && c.fallback != nil && (errors.Is(err, ErrKvliteTimeout) || errors.Is(err, ErrKvliteConnError)) {
			if c.metrics != nil {
				c.metrics.IncKvliteFallbackCount()
			}
			return c.fallback.StoreSummary(ctx, summary, ttl)
		}
		return err
	}
	return nil
}

func (c *compositeStore) AllocateTurnSeq(ctx context.Context, sessionID string) (int64, error) {
	if c == nil || c.primary == nil {
		return 0, errors.New("session store not initialized")
	}
	seq, err := c.primary.AllocateTurnSeq(ctx, sessionID)
	if err == nil {
		return seq, nil
	}
	if c.fallbackEnable && c.fallback != nil {
		if errors.Is(err, ErrKvliteTimeout) || errors.Is(err, ErrKvliteConnError) {
			if c.metrics != nil {
				c.metrics.IncKvliteFallbackCount()
			}
			return c.fallback.AllocateTurnSeq(ctx, sessionID)
		}
	}
	return 0, err
}

func (c *compositeStore) WriteOpenTurn(ctx context.Context, turn *TurnRecord, ttl time.Duration) error {
	if c == nil || c.primary == nil {
		return errors.New("session store not initialized")
	}
	if err := c.primary.WriteOpenTurn(ctx, turn, ttl); err != nil {
		if c.fallbackEnable && c.fallback != nil && (errors.Is(err, ErrKvliteTimeout) || errors.Is(err, ErrKvliteConnError)) {
			if c.metrics != nil {
				c.metrics.IncKvliteFallbackCount()
			}
			return c.fallback.WriteOpenTurn(ctx, turn, ttl)
		}
		return err
	}
	return nil
}

func (c *compositeStore) CompleteTurn(ctx context.Context, sessionID string, seq int64, assistantMessageID string, assistantMessage openai.ChatCompletionMessage, assistantTokens int, ttl time.Duration) error {
	if c == nil || c.primary == nil {
		return errors.New("session store not initialized")
	}
	if err := c.primary.CompleteTurn(ctx, sessionID, seq, assistantMessageID, assistantMessage, assistantTokens, ttl); err != nil {
		if c.fallbackEnable && c.fallback != nil && (errors.Is(err, ErrKvliteTimeout) || errors.Is(err, ErrKvliteConnError)) {
			if c.metrics != nil {
				c.metrics.IncKvliteFallbackCount()
			}
			return c.fallback.CompleteTurn(ctx, sessionID, seq, assistantMessageID, assistantMessage, assistantTokens, ttl)
		}
		return err
	}
	return nil
}

func (c *compositeStore) WriteOpenTurnWithMeta(ctx context.Context, turn *TurnRecord, ttl time.Duration) (*SessionMeta, error) {
	if c == nil || c.primary == nil {
		return nil, errors.New("session store not initialized")
	}
	if writer, ok := c.primary.(openTurnWriter); ok {
		meta, err := writer.WriteOpenTurnWithMeta(ctx, turn, ttl)
		if err == nil || !c.fallbackEnable || c.fallback == nil {
			return meta, err
		}
		if errors.Is(err, ErrKvliteTimeout) || errors.Is(err, ErrKvliteConnError) {
			if c.metrics != nil {
				c.metrics.IncKvliteFallbackCount()
			}
			if fallbackWriter, ok := c.fallback.(openTurnWriter); ok {
				return fallbackWriter.WriteOpenTurnWithMeta(ctx, turn, ttl)
			}
			return nil, c.fallback.WriteOpenTurn(ctx, turn, ttl)
		}
		return meta, err
	}
	if err := c.primary.WriteOpenTurn(ctx, turn, ttl); err != nil {
		return nil, err
	}
	meta, metaErr := c.primary.LoadSessionMeta(ctx, turn.SessionID)
	if metaErr != nil {
		return nil, metaErr
	}
	return meta, nil
}

func (c *compositeStore) CompleteTurnWithTurn(ctx context.Context, turn *TurnRecord, meta *SessionMeta, assistantMessageID string, assistantMessage openai.ChatCompletionMessage, assistantTokens int, ttl time.Duration) error {
	if c == nil || c.primary == nil {
		return errors.New("session store not initialized")
	}
	if writer, ok := c.primary.(completeTurnWriter); ok {
		err := writer.CompleteTurnWithTurn(ctx, turn, meta, assistantMessageID, assistantMessage, assistantTokens, ttl)
		if err == nil || !c.fallbackEnable || c.fallback == nil {
			return err
		}
		if errors.Is(err, ErrKvliteTimeout) || errors.Is(err, ErrKvliteConnError) {
			if c.metrics != nil {
				c.metrics.IncKvliteFallbackCount()
			}
			if fallbackWriter, ok := c.fallback.(completeTurnWriter); ok {
				return fallbackWriter.CompleteTurnWithTurn(ctx, turn, meta, assistantMessageID, assistantMessage, assistantTokens, ttl)
			}
			if turn != nil {
				return c.fallback.CompleteTurn(ctx, turn.SessionID, turn.Seq, assistantMessageID, assistantMessage, assistantTokens, ttl)
			}
		}
		return err
	}
	if turn == nil {
		return errors.New("turn is nil")
	}
	return c.primary.CompleteTurn(ctx, turn.SessionID, turn.Seq, assistantMessageID, assistantMessage, assistantTokens, ttl)
}

func NewStore(cnf *config.Config, logger log.ILogger, metrics *metrics_bus.BusMetrics) (SessionStore, error) {
	if cnf == nil {
		return nil, errors.New("config is nil")
	}
	namespace := cnf.Context.Namespace
	if namespace == "" {
		namespace = "ai:chat"
	}
	backend := cnf.Context.Backend
	if backend == "" {
		backend = "redis"
	}
	contextTTL := time.Duration(cnf.Chat.ContextTTL) * time.Second
	if contextTTL <= 0 {
		contextTTL = 30 * time.Minute
	}
	if logger != nil {
		logger.InfoF("context backend config backend=%s fallback_to_redis=%t namespace=%s", backend, cnf.Context.FallbackToRedis, namespace)
	}

	redisStore := newSessionStore(newRedisRawBackend(cnf, namespace), namespace, metrics)
	if backend == "redis" {
		return redisStore, nil
	}

	if backend == "kvlite" {
		kvliteStore := newSessionStore(newKVLiteRawBackend(cnf, namespace, metrics), namespace, metrics)
		if cnf.Context.FallbackToRedis {
			return &compositeStore{
				primary:        kvliteStore,
				fallback:       redisStore,
				fallbackEnable: true,
				metrics:        metrics,
				logger:         logger,
				name:           "kvlite+redis",
			}, nil
		}
		return kvliteStore, nil
	}

	if backend == "legacy_redis" {
		return redisStore, nil
	}

	return nil, fmt.Errorf("unsupported context backend %q", backend)
}

func (c *compositeStore) shadowCompareTurns(sessionID string, afterSeq int64, limit int, primaryTurns []*TurnRecord) {
	if c == nil || !c.compareEnabled || !c.shadowRead || c.fallback == nil {
		return
	}
	if c.metrics != nil {
		c.metrics.IncKvliteShadowReadCount()
	}
	go func() {
		shadowCtx, cancel := context.WithTimeout(context.Background(), c.compareTimeout)
		defer cancel()
		fallbackTurns, err := c.fallback.LoadTurnsSince(shadowCtx, sessionID, afterSeq, limit)
		if err != nil {
			return
		}
		if ok, detail := turnsMatch(primaryTurns, fallbackTurns); !ok {
			c.recordMismatch("turns", sessionID, detail)
			if c.repairOnMismatch {
				c.shadowRepairTurns(sessionID, fallbackTurns)
			}
		}
	}()
}

func (c *compositeStore) shadowCompareMeta(sessionID string, primaryMeta *SessionMeta) {
	if c == nil || !c.compareEnabled || !c.shadowRead || c.fallback == nil {
		return
	}
	if c.metrics != nil {
		c.metrics.IncKvliteShadowReadCount()
	}
	go func() {
		shadowCtx, cancel := context.WithTimeout(context.Background(), c.compareTimeout)
		defer cancel()
		fallbackMeta, err := c.fallback.LoadSessionMeta(shadowCtx, sessionID)
		if err != nil {
			return
		}
		if ok, detail := sessionMetaMatch(primaryMeta, fallbackMeta); !ok {
			c.recordMismatch("meta", sessionID, detail)
			if c.repairOnMismatch {
				c.shadowRepairMeta(sessionID, fallbackMeta)
			}
		}
	}()
}

func (c *compositeStore) shadowCompareSummary(sessionID string, primarySummary *SummaryRecord) {
	if c == nil || !c.compareEnabled || !c.shadowRead || c.fallback == nil {
		return
	}
	if c.metrics != nil {
		c.metrics.IncKvliteShadowReadCount()
	}
	go func() {
		shadowCtx, cancel := context.WithTimeout(context.Background(), c.compareTimeout)
		defer cancel()
		fallbackSummary, err := c.fallback.LoadSummary(shadowCtx, sessionID)
		if err != nil {
			return
		}
		if ok, detail := summaryMatch(primarySummary, fallbackSummary); !ok {
			c.recordMismatch("summary", sessionID, detail)
			if c.repairOnMismatch {
				c.shadowRepairSummary(sessionID, fallbackSummary)
			}
		}
	}()
}

func (c *compositeStore) shadowRepairTurns(sessionID string, turns []*TurnRecord) {
	if c == nil || c.primary == nil || len(turns) == 0 {
		return
	}
	repairCtx, cancel := context.WithTimeout(context.Background(), c.compareTimeout)
	defer cancel()
	for _, turn := range turns {
		if turn == nil {
			continue
		}
		if err := c.repairTurn(repairCtx, turn); err != nil {
			c.recordRepairError("turns", sessionID, err)
			return
		}
	}
	if c.metrics != nil {
		c.metrics.IncKvliteCompareRepairCount()
	}
}

func (c *compositeStore) shadowRepairMeta(sessionID string, meta *SessionMeta) {
	if c == nil || c.primary == nil || meta == nil {
		return
	}
	repairCtx, cancel := context.WithTimeout(context.Background(), c.compareTimeout)
	defer cancel()
	writer, ok := c.primary.(metaWriter)
	if !ok {
		return
	}
	if err := writer.storeMeta(repairCtx, cloneSessionMeta(meta), c.contextTTL); err != nil {
		c.recordRepairError("meta", sessionID, err)
		return
	}
	if c.metrics != nil {
		c.metrics.IncKvliteCompareRepairCount()
	}
}

func (c *compositeStore) shadowRepairSummary(sessionID string, summary *SummaryRecord) {
	if c == nil || c.primary == nil || summary == nil {
		return
	}
	repairCtx, cancel := context.WithTimeout(context.Background(), c.compareTimeout)
	defer cancel()
	ttl := c.summaryTTL
	if ttl <= 0 {
		ttl = c.contextTTL
	}
	if err := c.primary.StoreSummary(repairCtx, cloneSummaryRecord(summary), ttl); err != nil {
		c.recordRepairError("summary", sessionID, err)
		return
	}
	if c.metrics != nil {
		c.metrics.IncKvliteCompareRepairCount()
	}
}

func (c *compositeStore) repairTurn(ctx context.Context, turn *TurnRecord) error {
	if c == nil || c.primary == nil || turn == nil {
		return nil
	}
	turnCopy := cloneTurnRecord(turn)
	wasCompleted := turnCopy.TurnStatus == TurnStatusCompleted
	hasAssistantContent := turnCopy.AssistantMessageID != "" || strings.TrimSpace(turnCopy.AssistantMessage.Content) != ""
	ttl := c.contextTTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if err := c.primary.WriteOpenTurn(ctx, turnCopy, ttl); err != nil {
		return err
	}
	if wasCompleted || hasAssistantContent {
		return c.primary.CompleteTurn(ctx, turnCopy.SessionID, turnCopy.Seq, turnCopy.AssistantMessageID, turnCopy.AssistantMessage, turnCopy.AssistantTokens, ttl)
	}
	return nil
}

func (c *compositeStore) recordMismatch(kind, sessionID, detail string) {
	if c.metrics != nil {
		c.metrics.IncKvliteCompareMismatchCount()
	}
	if c.logger != nil {
		c.logger.WarningF("kvlite compare mismatch kind=%s session_id=%s detail=%s", kind, sessionID, detail)
	}
}

func (c *compositeStore) recordRepairError(kind, sessionID string, err error) {
	if err == nil {
		return
	}
	if c.logger != nil {
		c.logger.WarningF("kvlite repair failed kind=%s session_id=%s err=%v", kind, sessionID, err)
	}
}

func cloneTurnRecord(turn *TurnRecord) *TurnRecord {
	if turn == nil {
		return nil
	}
	copyTurn := *turn
	return &copyTurn
}

func cloneTurns(turns []*TurnRecord) []*TurnRecord {
	if len(turns) == 0 {
		return nil
	}
	cloned := make([]*TurnRecord, 0, len(turns))
	for _, turn := range turns {
		cloned = append(cloned, cloneTurnRecord(turn))
	}
	return cloned
}

func cloneSessionMeta(meta *SessionMeta) *SessionMeta {
	if meta == nil {
		return nil
	}
	copyMeta := *meta
	return &copyMeta
}

func cloneSummaryRecord(summary *SummaryRecord) *SummaryRecord {
	if summary == nil {
		return nil
	}
	copySummary := *summary
	return &copySummary
}

func turnsMatch(left, right []*TurnRecord) (bool, string) {
	if len(left) != len(right) {
		return false, fmt.Sprintf("count mismatch left=%d right=%d", len(left), len(right))
	}
	for idx := range left {
		if !turnMatch(left[idx], right[idx]) {
			return false, fmt.Sprintf("turn mismatch index=%d seq_left=%d seq_right=%d", idx, turnSeq(left[idx]), turnSeq(right[idx]))
		}
	}
	return true, ""
}

func turnMatch(left, right *TurnRecord) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.SessionID == right.SessionID &&
		left.Seq == right.Seq &&
		left.TurnStatus == right.TurnStatus &&
		left.RequestMessageID == right.RequestMessageID &&
		left.ParentMessageID == right.ParentMessageID &&
		left.AssistantMessageID == right.AssistantMessageID &&
		left.UserMessage.Role == right.UserMessage.Role &&
		left.UserMessage.Content == right.UserMessage.Content &&
		left.AssistantMessage.Role == right.AssistantMessage.Role &&
		left.AssistantMessage.Content == right.AssistantMessage.Content &&
		left.UserTokens == right.UserTokens &&
		left.AssistantTokens == right.AssistantTokens &&
		left.TotalTokens == right.TotalTokens
}

func turnSeq(turn *TurnRecord) int64 {
	if turn == nil {
		return 0
	}
	return turn.Seq
}

func sessionMetaMatch(left, right *SessionMeta) (bool, string) {
	if left == nil || right == nil {
		return left == right, "meta missing"
	}
	if left.SessionID != right.SessionID {
		return false, fmt.Sprintf("session_id mismatch left=%s right=%s", left.SessionID, right.SessionID)
	}
	if left.HeadSeq != right.HeadSeq || left.LastSeq != right.LastSeq || left.TurnCount != right.TurnCount || left.SummarySeq != right.SummarySeq || left.SummaryRef != right.SummaryRef || left.LastMessageID != right.LastMessageID || left.CompatMode != right.CompatMode {
		return false, fmt.Sprintf("meta fields mismatch left=%+v right=%+v", left, right)
	}
	return true, ""
}

func summaryMatch(left, right *SummaryRecord) (bool, string) {
	if left == nil || right == nil {
		return left == right, "summary missing"
	}
	if left.SessionID != right.SessionID || left.SummarySeq != right.SummarySeq || left.SummarizedRangeBegin != right.SummarizedRangeBegin || left.SummarizedRangeEnd != right.SummarizedRangeEnd || left.SummaryText != right.SummaryText || left.SummaryTokens != right.SummaryTokens || left.SourceTurnCount != right.SourceTurnCount || left.CompressionMode != right.CompressionMode {
		return false, fmt.Sprintf("summary fields mismatch left=%+v right=%+v", left, right)
	}
	return true, ""
}
