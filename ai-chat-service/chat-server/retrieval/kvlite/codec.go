// Package kvlite implements the wire-safe value codec used by the AI-chat
// KVLite vector backend. KVLite's current VSEARCH response is a single
// delimiter-separated string, so every field carried through that response
// must be encoded before it is sent to KVLite.
package kvlite

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// AnswerEnvelopeVersion is the only answer payload version accepted by the
	// initial vector backend implementation.
	AnswerEnvelopeVersion = 1

	// DefaultMaxAnswerBytes caps the original answer before JSON and Base64URL
	// encoding. The final encoded payload is capped separately because JSON
	// escaping can expand some valid text before Base64 is applied.
	DefaultMaxAnswerBytes = 384 * 1024

	// DefaultMaxEncodedAnswerBytes leaves room below KVLite's default 2 MiB
	// response limit for several VSEARCH hits and their protocol overhead.
	DefaultMaxEncodedAnswerBytes = 520 * 1024
)

var (
	ErrInvalidScope          = errors.New("invalid KVLite logical scope")
	ErrInvalidQuestion       = errors.New("invalid KVLite encoded question")
	ErrInvalidAnswerEnvelope = errors.New("invalid KVLite answer envelope")
	ErrAnswerTooLarge        = errors.New("KVLite answer exceeds application size limit")
	ErrInvalidVSearchPayload = errors.New("invalid KVLite VSEARCH payload")
)

// LogicalScope separates knowledge domains before it is Base64URL encoded for
// KVLite. All three base fields are mandatory. Provider/model/prompt version
// are optional compatibility boundaries for answers tied to a model policy.
type LogicalScope struct {
	Tenant        string `json:"tenant"`
	KnowledgeBase string `json:"knowledge_base"`
	CorpusVersion string `json:"corpus_version"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	PromptVersion string `json:"prompt_version,omitempty"`
}

// AnswerEnvelope is stored in KVLite as Base64URL(JSON). Answer is not used as
// embedding input, so encoding it does not affect semantic retrieval quality.
type AnswerEnvelope struct {
	Version    int    `json:"version"`
	DocumentID string `json:"document_id"`
	Answer     string `json:"answer"`
	Source     string `json:"source"`
	CreatedAt  int64  `json:"created_at"`
}

// SearchHit is a decoded, validated record returned by VSEARCH.
type SearchHit struct {
	Score    float64
	Scope    LogicalScope
	Question string
	Answer   AnswerEnvelope
}

// EncodeScope produces a canonical unpadded Base64URL JSON scope.
func EncodeScope(scope LogicalScope) (string, error) {
	normalized, err := normalizeScope(scope)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("marshal logical scope: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// DecodeScope accepts only the canonical encoding emitted by EncodeScope.
func DecodeScope(encoded string) (LogicalScope, error) {
	payload, err := decodeCanonicalBase64URL(encoded)
	if err != nil {
		return LogicalScope{}, fmt.Errorf("%w: scope encoding: %v", ErrInvalidScope, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var scope LogicalScope
	if err := decoder.Decode(&scope); err != nil {
		return LogicalScope{}, fmt.Errorf("%w: scope JSON: %v", ErrInvalidScope, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return LogicalScope{}, fmt.Errorf("%w: multiple scope JSON values", ErrInvalidScope)
	}
	return normalizeScope(scope)
}

// EncodeQuestion escapes only characters that can make the VSEARCH response
// ambiguous. Backslashes are escaped first so decoding remains reversible.
func EncodeQuestion(question string) (string, error) {
	if strings.TrimSpace(question) == "" || !utf8.ValidString(question) {
		return "", ErrInvalidQuestion
	}
	encoded := strings.ReplaceAll(question, "\\", "\\\\")
	encoded = strings.ReplaceAll(encoded, "|", "\\p")
	return encoded, nil
}

// DecodeQuestion reverses EncodeQuestion and rejects unknown escape sequences.
func DecodeQuestion(encoded string) (string, error) {
	if encoded == "" || !utf8.ValidString(encoded) {
		return "", ErrInvalidQuestion
	}
	var builder strings.Builder
	builder.Grow(len(encoded))
	for index := 0; index < len(encoded); index++ {
		ch := encoded[index]
		if ch != '\\' {
			if ch == '|' {
				return "", fmt.Errorf("%w: unescaped delimiter", ErrInvalidQuestion)
			}
			builder.WriteByte(ch)
			continue
		}
		if index+1 >= len(encoded) {
			return "", fmt.Errorf("%w: trailing escape", ErrInvalidQuestion)
		}
		index++
		switch encoded[index] {
		case '\\':
			builder.WriteByte('\\')
		case 'p':
			builder.WriteByte('|')
		default:
			return "", fmt.Errorf("%w: unknown escape", ErrInvalidQuestion)
		}
	}
	decoded := builder.String()
	if strings.TrimSpace(decoded) == "" || !utf8.ValidString(decoded) {
		return "", ErrInvalidQuestion
	}
	return decoded, nil
}

// EncodeAnswer serializes an answer envelope and enforces the application
// answer cap before the 4/3 Base64 expansion is sent to KVLite.
func EncodeAnswer(envelope AnswerEnvelope) (string, error) {
	normalized, err := normalizeAnswerEnvelope(envelope)
	if err != nil {
		return "", err
	}
	if len(normalized.Answer) > DefaultMaxAnswerBytes {
		return "", fmt.Errorf("%w: %d > %d bytes", ErrAnswerTooLarge, len(normalized.Answer), DefaultMaxAnswerBytes)
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("marshal answer envelope: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	if len(encoded) > DefaultMaxEncodedAnswerBytes {
		return "", fmt.Errorf("%w: encoded payload %d > %d bytes", ErrAnswerTooLarge, len(encoded), DefaultMaxEncodedAnswerBytes)
	}
	return encoded, nil
}

// DecodeAnswer accepts only the canonical encoding emitted by EncodeAnswer.
func DecodeAnswer(encoded string) (AnswerEnvelope, error) {
	payload, err := decodeCanonicalBase64URL(encoded)
	if err != nil {
		return AnswerEnvelope{}, fmt.Errorf("%w: answer encoding: %v", ErrInvalidAnswerEnvelope, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var envelope AnswerEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return AnswerEnvelope{}, fmt.Errorf("%w: answer JSON: %v", ErrInvalidAnswerEnvelope, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return AnswerEnvelope{}, fmt.Errorf("%w: multiple answer JSON values", ErrInvalidAnswerEnvelope)
	}
	return normalizeAnswerEnvelope(envelope)
}

// ParseVSearchItem decodes one current KVLite VSEARCH array item. It accepts
// only the exact legacy field order and values produced by this package.
func ParseVSearchItem(raw string) (SearchHit, error) {
	parts := strings.Split(raw, "|")
	if len(parts) != 4 || !strings.HasPrefix(parts[0], "score:") ||
		!strings.HasPrefix(parts[1], "scope:") ||
		!strings.HasPrefix(parts[2], "question:") ||
		!strings.HasPrefix(parts[3], "answer:") {
		return SearchHit{}, fmt.Errorf("%w: expected score/scope/question/answer fields", ErrInvalidVSearchPayload)
	}
	scoreText := strings.TrimPrefix(parts[0], "score:")
	score, err := strconv.ParseFloat(scoreText, 64)
	if err != nil || math.IsNaN(score) || math.IsInf(score, 0) {
		return SearchHit{}, fmt.Errorf("%w: invalid score", ErrInvalidVSearchPayload)
	}
	scope, err := DecodeScope(strings.TrimPrefix(parts[1], "scope:"))
	if err != nil {
		return SearchHit{}, fmt.Errorf("%w: %v", ErrInvalidVSearchPayload, err)
	}
	question, err := DecodeQuestion(strings.TrimPrefix(parts[2], "question:"))
	if err != nil {
		return SearchHit{}, fmt.Errorf("%w: %v", ErrInvalidVSearchPayload, err)
	}
	answer, err := DecodeAnswer(strings.TrimPrefix(parts[3], "answer:"))
	if err != nil {
		return SearchHit{}, fmt.Errorf("%w: %v", ErrInvalidVSearchPayload, err)
	}
	return SearchHit{Score: score, Scope: scope, Question: question, Answer: answer}, nil
}

func normalizeScope(scope LogicalScope) (LogicalScope, error) {
	scope.Tenant = strings.TrimSpace(scope.Tenant)
	scope.KnowledgeBase = strings.TrimSpace(scope.KnowledgeBase)
	scope.CorpusVersion = strings.TrimSpace(scope.CorpusVersion)
	scope.Provider = strings.TrimSpace(scope.Provider)
	scope.Model = strings.TrimSpace(scope.Model)
	scope.PromptVersion = strings.TrimSpace(scope.PromptVersion)
	if scope.Tenant == "" || scope.KnowledgeBase == "" || scope.CorpusVersion == "" {
		return LogicalScope{}, fmt.Errorf("%w: tenant, knowledge_base and corpus_version are required", ErrInvalidScope)
	}
	if !utf8.ValidString(scope.Tenant) || !utf8.ValidString(scope.KnowledgeBase) ||
		!utf8.ValidString(scope.CorpusVersion) || !utf8.ValidString(scope.Provider) ||
		!utf8.ValidString(scope.Model) || !utf8.ValidString(scope.PromptVersion) {
		return LogicalScope{}, fmt.Errorf("%w: invalid UTF-8", ErrInvalidScope)
	}
	return scope, nil
}

func normalizeAnswerEnvelope(envelope AnswerEnvelope) (AnswerEnvelope, error) {
	envelope.DocumentID = strings.TrimSpace(envelope.DocumentID)
	envelope.Source = strings.TrimSpace(envelope.Source)
	if envelope.Version != AnswerEnvelopeVersion || envelope.DocumentID == "" || envelope.Answer == "" || envelope.Source == "" ||
		!utf8.ValidString(envelope.DocumentID) || !utf8.ValidString(envelope.Answer) || !utf8.ValidString(envelope.Source) {
		return AnswerEnvelope{}, ErrInvalidAnswerEnvelope
	}
	return envelope, nil
}

func decodeCanonicalBase64URL(encoded string) ([]byte, error) {
	if encoded == "" || strings.Contains(encoded, "|") {
		return nil, errors.New("empty or delimiter-containing Base64URL value")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	if base64.RawURLEncoding.EncodeToString(payload) != encoded {
		return nil, errors.New("non-canonical Base64URL value")
	}
	return payload, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
