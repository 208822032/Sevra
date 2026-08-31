package kvlite

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAddress = "127.0.0.1:28080"

	requiredVectorDimension = "512"
	requiredVectorIndex     = "core_hnsw_v2"
	requiredVectorMetric    = "ip"
)

var (
	ErrNotReady = errors.New("KVLite vector backend is not ready")
	ErrNotFound = errors.New("KVLite vector document not found")
)

// Config controls the dedicated V-command client. It intentionally contains
// no normal-key options and does not share chat-context connections.
type Config struct {
	Address        string
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	PoolSize       int
	MaxIdle        int
}

func DefaultConfig(address string) Config {
	if strings.TrimSpace(address) == "" {
		address = DefaultAddress
	}
	return Config{
		Address:        address,
		ConnectTimeout: time.Second,
		ReadTimeout:    5 * time.Second,
		WriteTimeout:   5 * time.Second,
		PoolSize:       4,
		MaxIdle:        2,
	}
}

func (c Config) normalized() (Config, error) {
	c.Address = strings.TrimSpace(c.Address)
	if c.Address == "" {
		return Config{}, fmt.Errorf("%w: missing address", ErrUnavailable)
	}
	if c.ConnectTimeout <= 0 || c.ReadTimeout <= 0 || c.WriteTimeout <= 0 || c.PoolSize <= 0 || c.MaxIdle < 0 || c.MaxIdle > c.PoolSize {
		return Config{}, fmt.Errorf("invalid KVLite vector client configuration")
	}
	return c, nil
}

type Document struct {
	Question string
	Scope    LogicalScope
	Answer   AnswerEnvelope
}

type Hit struct {
	Document
	Score float64
}

// Store is the sole vector backend contract used by later routing stages.
type Store interface {
	Upsert(ctx context.Context, doc Document) error
	Get(ctx context.Context, question string, scope LogicalScope) (*Document, error)
	Search(ctx context.Context, query string, scope LogicalScope, topK int, threshold float64) ([]Hit, error)
	Delete(ctx context.Context, question string, scope LogicalScope) error
	Health(ctx context.Context) error
	Close() error
}

type KVLiteVectorStore struct {
	config Config
	pool   *respPool
}

func NewStore(config Config) (*KVLiteVectorStore, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	return &KVLiteVectorStore{
		config: normalized,
		pool:   newRESPPool(normalized.Address, normalized.ConnectTimeout, normalized.PoolSize, normalized.MaxIdle),
	}, nil
}

func (s *KVLiteVectorStore) Close() error {
	if s != nil && s.pool != nil {
		s.pool.close()
	}
	return nil
}

func (s *KVLiteVectorStore) Upsert(ctx context.Context, doc Document) error {
	question, scope, answer, err := encodeDocument(doc)
	if err != nil {
		return err
	}
	response, err := s.exec(ctx, "VSET", question, answer, scope)
	if err != nil {
		return err
	}
	if response.text != "OK" || response.isNil || response.array != nil {
		return fmt.Errorf("%w: VSET expected +OK", ErrProtocol)
	}
	return nil
}

func (s *KVLiteVectorStore) Get(ctx context.Context, question string, scope LogicalScope) (*Document, error) {
	normalizedScope, err := normalizeScope(scope)
	if err != nil {
		return nil, err
	}
	encodedQuestion, encodedScope, err := encodeQuestionScope(question, normalizedScope)
	if err != nil {
		return nil, err
	}
	response, err := s.exec(ctx, "VGET", encodedQuestion, encodedScope)
	if err != nil {
		return nil, err
	}
	if response.isNil || response.text == "NO EXIST" {
		return nil, nil
	}
	if response.array != nil {
		return nil, fmt.Errorf("%w: VGET expected bulk answer", ErrProtocol)
	}
	answer, err := DecodeAnswer(response.text)
	if err != nil {
		return nil, fmt.Errorf("%w: VGET answer: %v", ErrProtocol, err)
	}
	return &Document{Question: question, Scope: normalizedScope, Answer: answer}, nil
}

func (s *KVLiteVectorStore) Search(ctx context.Context, query string, scope LogicalScope, topK int, threshold float64) ([]Hit, error) {
	if topK <= 0 || topK > 1024 || math.IsNaN(threshold) || math.IsInf(threshold, 0) {
		return nil, fmt.Errorf("invalid KVLite vector search arguments")
	}
	normalizedScope, err := normalizeScope(scope)
	if err != nil {
		return nil, err
	}
	encodedQuery, encodedScope, err := encodeQuestionScope(query, normalizedScope)
	if err != nil {
		return nil, err
	}
	response, err := s.exec(ctx, "VSEARCH", encodedQuery, encodedScope, strconv.Itoa(topK), strconv.FormatFloat(threshold, 'f', -1, 64))
	if err != nil {
		return nil, err
	}
	if response.isNil {
		return []Hit{}, nil
	}
	if response.array == nil {
		return nil, fmt.Errorf("%w: VSEARCH expected array", ErrProtocol)
	}
	hits := make([]Hit, 0, len(response.array))
	for _, item := range response.array {
		if item.isNil || item.array != nil {
			return nil, fmt.Errorf("%w: VSEARCH item is not a bulk string", ErrProtocol)
		}
		decoded, err := ParseVSearchItem(item.text)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrProtocol, err)
		}
		if decoded.Scope != normalizedScope {
			return nil, fmt.Errorf("%w: VSEARCH returned a different logical scope", ErrProtocol)
		}
		if decoded.Score < threshold {
			return nil, fmt.Errorf("%w: VSEARCH returned score below threshold", ErrProtocol)
		}
		hits = append(hits, Hit{Document: Document{Question: decoded.Question, Scope: decoded.Scope, Answer: decoded.Answer}, Score: decoded.Score})
	}
	return hits, nil
}

func (s *KVLiteVectorStore) Delete(ctx context.Context, question string, scope LogicalScope) error {
	normalizedScope, err := normalizeScope(scope)
	if err != nil {
		return err
	}
	encodedQuestion, encodedScope, err := encodeQuestionScope(question, normalizedScope)
	if err != nil {
		return err
	}
	response, err := s.exec(ctx, "VDEL", encodedQuestion, encodedScope)
	if err != nil {
		return err
	}
	if response.text == "NO EXIST" {
		return ErrNotFound
	}
	if response.text != "OK" || response.isNil || response.array != nil {
		return fmt.Errorf("%w: VDEL expected +OK", ErrProtocol)
	}
	return nil
}

func (s *KVLiteVectorStore) Health(ctx context.Context) error {
	response, err := s.exec(ctx, "VINFO")
	if err != nil {
		return err
	}
	if response.isNil || response.array == nil {
		return fmt.Errorf("%w: VINFO expected array", ErrProtocol)
	}
	info := make(map[string]string, len(response.array))
	for _, item := range response.array {
		if item.isNil || item.array != nil {
			return fmt.Errorf("%w: VINFO item is not a string", ErrProtocol)
		}
		key, value, ok := strings.Cut(item.text, ":")
		if !ok || key == "" {
			return fmt.Errorf("%w: malformed VINFO item", ErrProtocol)
		}
		if _, exists := info[key]; exists {
			return fmt.Errorf("%w: duplicate VINFO key %q", ErrProtocol, key)
		}
		info[key] = value
	}
	// Current KVLite VINFO does not emit enabled. A successful VINFO can only
	// be produced after vector_enabled is checked in its command handler, so it
	// is the compatible enabled=1 proof. If a future version emits it, validate
	// it explicitly.
	if enabled, present := info["enabled"]; present && enabled != "1" {
		return fmt.Errorf("%w: enabled=%q", ErrNotReady, enabled)
	}
	for key, want := range map[string]string{
		"index_ready": "1",
		"index_type":  requiredVectorIndex,
		"dimension":   requiredVectorDimension,
		"metric":      requiredVectorMetric,
	} {
		if got := info[key]; got != want {
			return fmt.Errorf("%w: %s=%q, want %q", ErrNotReady, key, got, want)
		}
	}
	return nil
}

func (s *KVLiteVectorStore) exec(ctx context.Context, command ...string) (respValue, error) {
	if s == nil || s.pool == nil {
		return respValue{}, fmt.Errorf("%w: store is not initialized", ErrUnavailable)
	}
	conn, err := s.pool.acquire(ctx)
	if err != nil {
		return respValue{}, err
	}
	response, err := conn.roundTrip(ctx, s.config.WriteTimeout, s.config.ReadTimeout, command...)
	s.pool.release(conn, err == nil)
	return response, err
}

func encodeDocument(doc Document) (question, scope, answer string, err error) {
	question, scope, err = encodeQuestionScope(doc.Question, doc.Scope)
	if err != nil {
		return "", "", "", err
	}
	answer, err = EncodeAnswer(doc.Answer)
	if err != nil {
		return "", "", "", err
	}
	return question, scope, answer, nil
}

func encodeQuestionScope(question string, scope LogicalScope) (encodedQuestion, encodedScope string, err error) {
	encodedQuestion, err = EncodeQuestion(question)
	if err != nil {
		return "", "", err
	}
	encodedScope, err = EncodeScope(scope)
	if err != nil {
		return "", "", err
	}
	return encodedQuestion, encodedScope, nil
}
