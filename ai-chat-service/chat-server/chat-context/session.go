package chat_context

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
)

type TurnStatus string

const (
	TurnStatusOpen      TurnStatus = "open"
	TurnStatusCompleted TurnStatus = "completed"
	TurnStatusFailed    TurnStatus = "failed"
)

type TurnRecord struct {
	SessionID          string                       `json:"session_id"`
	Seq                int64                        `json:"seq"`
	TurnStatus         TurnStatus                   `json:"turn_status"`
	RequestMessageID   string                       `json:"request_message_id,omitempty"`
	ParentMessageID    string                       `json:"parent_message_id,omitempty"`
	AssistantMessageID string                       `json:"assistant_message_id,omitempty"`
	UserMessage        openai.ChatCompletionMessage `json:"user_message"`
	AssistantMessage   openai.ChatCompletionMessage `json:"assistant_message,omitempty"`
	UserTokens         int                          `json:"user_tokens"`
	AssistantTokens    int                          `json:"assistant_tokens,omitempty"`
	TotalTokens        int                          `json:"total_tokens"`
	CreatedAtMS        int64                        `json:"created_at_ms"`
	UpdatedAtMS        int64                        `json:"updated_at_ms"`
}

type SessionMeta struct {
	SessionID     string `json:"session_id"`
	HeadSeq       int64  `json:"head_seq"`
	LastSeq       int64  `json:"last_seq"`
	TurnCount     int64  `json:"turn_count"`
	UpdatedAtMS   int64  `json:"updated_at_ms"`
	TTLSeconds    int    `json:"ttl_sec"`
	ExpiresAtMS   int64  `json:"expires_at_ms"`
	CompatMode    string `json:"compat_mode"`
	SummaryRef    string `json:"summary_ref,omitempty"`
	SummarySeq    int64  `json:"summary_seq,omitempty"`
	LastMessageID string `json:"last_message_id,omitempty"`
}

type SummaryRecord struct {
	SessionID             string `json:"session_id"`
	SummarySeq            int64  `json:"summary_seq"`
	SummarizedRangeBegin  int64  `json:"summarized_range_begin"`
	SummarizedRangeEnd    int64  `json:"summarized_range_end"`
	SummaryText           string `json:"summary_text"`
	SummaryTokens         int    `json:"summary_tokens"`
	SourceTurnCount       int64  `json:"source_turn_count"`
	CompressionMode       string `json:"compression_mode,omitempty"`
	CreatedAtMS           int64  `json:"created_at_ms"`
	UpdatedAtMS           int64  `json:"updated_at_ms"`
	ExpiresAtMS           int64  `json:"expires_at_ms"`
}

func (m *SessionMeta) normalize() {
	if m == nil {
		return
	}
	if m.CompatMode == "" {
		m.CompatMode = "session_seq"
	}
}

func newSessionID() string {
	return uuid.NewString()
}

func nowMS() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}

func ttlToMS(ttlSeconds int) int64 {
	if ttlSeconds <= 0 {
		return 0
	}
	return int64(ttlSeconds) * int64(time.Second/time.Millisecond)
}

func turnKey(namespace, sessionID string, seq int64) string {
	return fmt.Sprintf("%s:sess:%s:turn:%010d", namespace, sessionID, seq)
}

func metaKey(namespace, sessionID string) string {
	return fmt.Sprintf("%s:sess:%s:meta", namespace, sessionID)
}

func summaryKey(namespace, sessionID string) string {
	return fmt.Sprintf("%s:sess:%s:summary", namespace, sessionID)
}

func compatMessageKey(namespace, messageID string) string {
	return fmt.Sprintf("%s:compat:msg:%s", namespace, messageID)
}

func compatParentKey(namespace, parentMessageID string) string {
	return fmt.Sprintf("%s:compat:pid:%s", namespace, parentMessageID)
}

func padSeq(seq int64) string {
	return fmt.Sprintf("%010d", seq)
}
