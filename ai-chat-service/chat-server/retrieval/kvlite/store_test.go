package kvlite

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-chat-service/chat-server/llm"
)

type fakeRESPServer struct {
	listener net.Listener
	handler  func([]string) string

	mu          sync.Mutex
	connections int
	commands    [][]string
	closed      chan struct{}
}

func newFakeRESPServer(t *testing.T, handler func([]string) string) *fakeRESPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &fakeRESPServer{listener: listener, handler: handler, closed: make(chan struct{})}
	go server.serve()
	t.Cleanup(server.close)
	return server
}

func (s *fakeRESPServer) serve() {
	defer close(s.closed)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.connections++
		s.mu.Unlock()
		go s.serveConn(conn)
	}
}

func (s *fakeRESPServer) serveConn(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		value, err := readRESPValue(reader)
		if err != nil {
			return
		}
		args := make([]string, 0, len(value.array))
		for _, item := range value.array {
			if item.isNil || item.array != nil {
				return
			}
			args = append(args, item.text)
		}
		s.mu.Lock()
		s.commands = append(s.commands, append([]string(nil), args...))
		s.mu.Unlock()
		_, _ = conn.Write([]byte(s.handler(args)))
	}
}

func (s *fakeRESPServer) address() string { return s.listener.Addr().String() }

func (s *fakeRESPServer) close() {
	_ = s.listener.Close()
	<-s.closed
}

func (s *fakeRESPServer) snapshot() (int, [][]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	commands := make([][]string, len(s.commands))
	for index, command := range s.commands {
		commands[index] = append([]string(nil), command...)
	}
	return s.connections, commands
}

func newTestStore(t *testing.T, address string) *KVLiteVectorStore {
	t.Helper()
	config := DefaultConfig(address)
	config.PoolSize = 1
	config.MaxIdle = 1
	store, err := NewStore(config)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testDocument() Document {
	return Document{
		Question: "如何处理 | 分隔符与 \\？",
		Scope: LogicalScope{
			Tenant: "tenant-a", KnowledgeBase: "help", CorpusVersion: "2026-08",
		},
		Answer: AnswerEnvelope{
			Version: AnswerEnvelopeVersion, DocumentID: "doc-1", Answer: "请在登录页点击忘记密码。", Source: "knowledge_base", CreatedAt: 1723881600,
		},
	}
}

func TestKVLiteVectorStoreVCommandRoundTripAndConnectionReuse(t *testing.T) {
	doc := testDocument()
	encodedScope, err := EncodeScope(doc.Scope)
	if err != nil {
		t.Fatal(err)
	}
	encodedQuestion, err := EncodeQuestion(doc.Question)
	if err != nil {
		t.Fatal(err)
	}
	encodedAnswer, err := EncodeAnswer(doc.Answer)
	if err != nil {
		t.Fatal(err)
	}
	searchItem := "score:0.932500|scope:" + encodedScope + "|question:" + encodedQuestion + "|answer:" + encodedAnswer
	server := newFakeRESPServer(t, func(args []string) string {
		switch args[0] {
		case "VSET", "VDEL":
			return "+OK\r\n"
		case "VGET":
			return respBulk(encodedAnswer)
		case "VSEARCH":
			return respArray(respBulk(searchItem))
		case "VINFO":
			return respArray(respBulk("index_ready:1"), respBulk("index_type:core_hnsw_v2"), respBulk("dimension:512"), respBulk("metric:ip"), respBulk("rebuild_error_message:"))
		default:
			return "-ERR unexpected command\r\n"
		}
	})
	store := newTestStore(t, server.address())
	ctx := context.Background()
	if err := store.Upsert(ctx, doc); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.Get(ctx, doc.Question, doc.Scope)
	if err != nil || got == nil || *got != doc {
		t.Fatalf("Get = %#v, %v; want %#v", got, err, doc)
	}
	hits, err := store.Search(ctx, doc.Question, doc.Scope, 3, 0.8)
	if err != nil || len(hits) != 1 || hits[0].Document != doc || hits[0].Score != 0.9325 {
		t.Fatalf("Search = %#v, %v", hits, err)
	}
	if err := store.Delete(ctx, doc.Question, doc.Scope); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}
	connections, commands := server.snapshot()
	if connections != 1 {
		t.Fatalf("connections = %d, want connection reuse", connections)
	}
	if len(commands) != 5 {
		t.Fatalf("command count = %d, want 5", len(commands))
	}
	if got, want := commands[0], []string{"VSET", encodedQuestion, encodedAnswer, encodedScope}; !sameStrings(got, want) {
		t.Fatalf("VSET = %#v, want %#v", got, want)
	}
	if got, want := commands[1], []string{"VGET", encodedQuestion, encodedScope}; !sameStrings(got, want) {
		t.Fatalf("VGET = %#v, want %#v", got, want)
	}
	if got, want := commands[2], []string{"VSEARCH", encodedQuestion, encodedScope, "3", "0.8"}; !sameStrings(got, want) {
		t.Fatalf("VSEARCH = %#v, want %#v", got, want)
	}
	if got, want := commands[3], []string{"VDEL", encodedQuestion, encodedScope}; !sameStrings(got, want) {
		t.Fatalf("VDEL = %#v, want %#v", got, want)
	}
	if got, want := commands[4], []string{"VINFO"}; !sameStrings(got, want) {
		t.Fatalf("VINFO = %#v, want %#v", got, want)
	}
}

func TestKVLiteVectorStoreRejectsMalformedSearchAndNotReadyInfo(t *testing.T) {
	doc := testDocument()
	server := newFakeRESPServer(t, func(args []string) string {
		switch args[0] {
		case "VSEARCH":
			return respArray(respBulk("score:bad|scope:invalid|question:q|answer:x"))
		case "VINFO":
			return respArray(respBulk("index_ready:0"), respBulk("index_type:core_hnsw_v2"), respBulk("dimension:512"), respBulk("metric:ip"))
		default:
			return "+NO EXIST\r\n"
		}
	})
	store := newTestStore(t, server.address())
	if _, err := store.Search(context.Background(), doc.Question, doc.Scope, 1, 0); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Search malformed error = %v, want ErrProtocol", err)
	}
	if err := store.Health(context.Background()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Health not ready error = %v, want ErrNotReady", err)
	}
}

func TestKVLiteVectorStoreSearchNormalizesScopeBeforeValidation(t *testing.T) {
	doc := testDocument()
	encodedScope, err := EncodeScope(doc.Scope)
	if err != nil {
		t.Fatal(err)
	}
	encodedQuestion, err := EncodeQuestion(doc.Question)
	if err != nil {
		t.Fatal(err)
	}
	encodedAnswer, err := EncodeAnswer(doc.Answer)
	if err != nil {
		t.Fatal(err)
	}
	server := newFakeRESPServer(t, func([]string) string {
		return respArray(respBulk("score:0.9|scope:" + encodedScope + "|question:" + encodedQuestion + "|answer:" + encodedAnswer))
	})
	store := newTestStore(t, server.address())
	paddedScope := doc.Scope
	paddedScope.Tenant = "  " + paddedScope.Tenant + "  "
	hits, err := store.Search(context.Background(), doc.Question, paddedScope, 1, 0.8)
	if err != nil || len(hits) != 1 || hits[0].Scope != doc.Scope {
		t.Fatalf("Search = %#v, %v", hits, err)
	}
}

func TestKVLiteVectorStoreMissingAndUnavailableAreNotSyntheticHits(t *testing.T) {
	doc := testDocument()
	server := newFakeRESPServer(t, func(args []string) string {
		if args[0] == "VGET" {
			return "+NO EXIST\r\n"
		}
		if args[0] == "VDEL" {
			return "+NO EXIST\r\n"
		}
		return "-ERR unexpected\r\n"
	})
	store := newTestStore(t, server.address())
	got, err := store.Get(context.Background(), doc.Question, doc.Scope)
	if err != nil || got != nil {
		t.Fatalf("Get missing = %#v, %v; want nil, nil", got, err)
	}
	if err := store.Delete(context.Background(), doc.Question, doc.Scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete missing error = %v, want ErrNotFound", err)
	}
	config := DefaultConfig("127.0.0.1:1")
	config.ConnectTimeout = 30 * time.Millisecond
	unavailable, err := NewStore(config)
	if err != nil {
		t.Fatal(err)
	}
	defer unavailable.Close()
	if _, err := unavailable.Get(context.Background(), doc.Question, doc.Scope); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable Get error = %v, want ErrUnavailable", err)
	} else if reason := FailureReason(err); reason != "kvlite_unavailable" {
		t.Fatalf("FailureReason = %q", reason)
	}
}

func TestKVLiteVectorStoreClassifiesUninitializedVectorAsNotReady(t *testing.T) {
	doc := testDocument()
	server := newFakeRESPServer(t, func([]string) string {
		return "-ERR vector not initialized\r\n"
	})
	store := newTestStore(t, server.address())
	if _, err := store.Get(context.Background(), doc.Question, doc.Scope); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Get error = %v, want ErrNotReady", err)
	} else if reason := FailureReason(err); reason != "kvlite_not_ready" {
		t.Fatalf("FailureReason = %q", reason)
	}
}

func TestAppendHitsToTracePreservesVectorIdentity(t *testing.T) {
	doc := testDocument()
	trace := &llm.AnswerTrace{}
	if err := AppendHitsToTrace(trace, []Hit{{Document: doc, Score: 0.91}}); err != nil {
		t.Fatal(err)
	}
	if len(trace.RetrievedHits) != 1 {
		t.Fatalf("trace hits = %d", len(trace.RetrievedHits))
	}
	hit := trace.RetrievedHits[0]
	if hit.DocumentID != doc.Answer.DocumentID || hit.RecordID != doc.Answer.DocumentID || hit.Score != 0.91 || hit.Source != doc.Answer.Source || hit.Scope == "" || hit.QuestionPreview != doc.Question || hit.AnswerPreview != doc.Answer.Answer {
		t.Fatalf("unexpected trace hit: %+v", hit)
	}
}

func respBulk(value string) string {
	return fmt.Sprintf("$%d\r\n%s\r\n", len(value), value)
}

func respArray(values ...string) string {
	return fmt.Sprintf("*%d\r\n%s", len(values), strings.Join(values, ""))
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
