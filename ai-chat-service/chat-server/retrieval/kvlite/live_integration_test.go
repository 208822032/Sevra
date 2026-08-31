package kvlite

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveKVLiteHealthAndScopedSearch is opt-in and read-only. It never calls
// VSET or VDEL, so it is safe against an operator-managed vector instance.
func TestLiveKVLiteHealthAndScopedSearch(t *testing.T) {
	address := os.Getenv("KVLITE_VECTOR_ADDR")
	if address == "" {
		t.Skip("set KVLITE_VECTOR_ADDR to run against a live KVLite instance")
	}
	config := DefaultConfig(address)
	config.ConnectTimeout = 3 * time.Second
	config.ReadTimeout = 10 * time.Second
	config.WriteTimeout = 3 * time.Second
	store, err := NewStore(config)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := store.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}
	_, err = store.Search(ctx, "KVLite 向量检索健康检查", LogicalScope{
		Tenant:        getenvDefault("KVLITE_VECTOR_TENANT", "local-dev"),
		KnowledgeBase: getenvDefault("KVLITE_VECTOR_KNOWLEDGE_BASE", "ai-chat"),
		CorpusVersion: getenvDefault("KVLITE_VECTOR_CORPUS_VERSION", "v1"),
	}, 1, 1)
	if err != nil {
		t.Fatalf("read-only VSEARCH: %v", err)
	}
}

// TestLiveKVLiteControlledWriteRoundTrip is intentionally more restrictive
// than the read-only test above. It writes one time-unique document only when
// the operator has explicitly opted in, and schedules VDEL immediately after
// the successful write.
func TestLiveKVLiteControlledWriteRoundTrip(t *testing.T) {
	if os.Getenv("KVLITE_VECTOR_WRITE_TEST") != "1" {
		t.Skip("set KVLITE_VECTOR_WRITE_TEST=1 to allow a temporary VSET/VDEL integration test")
	}
	address := os.Getenv("KVLITE_VECTOR_ADDR")
	if address == "" {
		t.Skip("set KVLITE_VECTOR_ADDR to run against a live KVLite instance")
	}
	config := DefaultConfig(address)
	config.ConnectTimeout = 3 * time.Second
	config.ReadTimeout = 10 * time.Second
	config.WriteTimeout = 3 * time.Second
	store, err := NewStore(config)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := store.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}
	scope := LogicalScope{
		Tenant:        getenvDefault("KVLITE_VECTOR_TENANT", "local-dev"),
		KnowledgeBase: getenvDefault("KVLITE_VECTOR_KNOWLEDGE_BASE", "ai-chat"),
		CorpusVersion: getenvDefault("KVLITE_VECTOR_CORPUS_VERSION", "v1"),
	}
	if err := cleanupStage5Probes(ctx, store, scope); err != nil {
		t.Fatalf("cleanup stale stage5 probes: %v", err)
	}
	stamp := time.Now().UnixNano()
	doc := Document{
		Question: fmt.Sprintf("KVLite 阶段五动态验证 %d", stamp),
		Scope:    scope,
		Answer: AnswerEnvelope{
			Version: AnswerEnvelopeVersion, DocumentID: fmt.Sprintf("stage5-probe-%d", stamp),
			Answer: "这是受控写入的临时动态验证文档。", Source: "knowledge_base", CreatedAt: time.Now().Unix(),
		},
	}
	writer, err := NewControlledWriter(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.UpsertApproved(ctx, ApprovedDocument{Document: doc, ApprovalID: fmt.Sprintf("stage5-test-%d", stamp)}); err != nil {
		t.Fatalf("UpsertApproved: %v", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.Delete(cleanupCtx, doc.Question, scope); err != nil {
			t.Errorf("cleanup VDEL for %s: %v", doc.Answer.DocumentID, err)
		}
	}()
	got, err := store.Get(ctx, doc.Question, scope)
	if err != nil || got == nil || *got != doc {
		t.Fatalf("VGET = %#v, %v; want %#v", got, err, doc)
	}
	var hits []Hit
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		hits, err = store.Search(ctx, doc.Question, scope, 1, 0.5)
		if err == nil && len(hits) > 0 {
			break
		}
	}
	if err != nil || len(hits) == 0 || hits[0].Answer.DocumentID != doc.Answer.DocumentID {
		t.Fatalf("VSEARCH = %#v, %v", hits, err)
	}
}

// TestLiveKVLiteSemanticScopeAndMarkdown is an opt-in product regression. It
// uses a unique logical scope and removes every test record before returning.
func TestLiveKVLiteSemanticScopeAndMarkdown(t *testing.T) {
	if os.Getenv("KVLITE_VECTOR_SEMANTIC_TEST") != "1" {
		t.Skip("set KVLITE_VECTOR_SEMANTIC_TEST=1 to run semantic/scope/Markdown integration")
	}
	address := os.Getenv("KVLITE_VECTOR_ADDR")
	if address == "" {
		t.Skip("set KVLITE_VECTOR_ADDR to run against a live KVLite instance")
	}
	config := DefaultConfig(address)
	config.ConnectTimeout, config.ReadTimeout, config.WriteTimeout = 3*time.Second, 10*time.Second, 3*time.Second
	store, err := NewStore(config)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := store.Health(ctx); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UnixNano()
	scope := LogicalScope{Tenant: fmt.Sprintf("stage6-tenant-%d", stamp), KnowledgeBase: "semantic-regression", CorpusVersion: "v1"}
	docs := []Document{
		{Question: "如何重置登录密码？", Scope: scope, Answer: AnswerEnvelope{Version: AnswerEnvelopeVersion, DocumentID: "stage6-password", Answer: "请在登录页点击 **忘记密码**。\n\n| 步骤 | 操作 |\n| --- | --- |\n| 1 | 完成身份验证 |\n| 2 | 设置新密码 |", Source: "knowledge_base", CreatedAt: time.Now().Unix()}},
		{Question: "如何修改绑定手机号？", Scope: scope, Answer: AnswerEnvelope{Version: AnswerEnvelopeVersion, DocumentID: "stage6-phone", Answer: "在账号安全页面更换绑定手机。", Source: "knowledge_base", CreatedAt: time.Now().Unix()}},
		{Question: "如何申请订单退款？", Scope: scope, Answer: AnswerEnvelope{Version: AnswerEnvelopeVersion, DocumentID: "stage6-refund", Answer: "在订单详情页提交退款申请。", Source: "knowledge_base", CreatedAt: time.Now().Unix()}},
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		for _, doc := range docs {
			if err := store.Delete(cleanupCtx, doc.Question, scope); err != nil {
				t.Errorf("cleanup %s: %v", doc.Answer.DocumentID, err)
			}
		}
	}()
	writer, err := NewControlledWriter(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range docs {
		if err := writer.UpsertApproved(ctx, ApprovedDocument{Document: doc, ApprovalID: "stage6-reviewed"}); err != nil {
			t.Fatalf("write %s: %v", doc.Answer.DocumentID, err)
		}
	}
	got, err := store.Get(ctx, docs[0].Question, scope)
	if err != nil || got == nil || got.Answer.Answer != docs[0].Answer.Answer {
		t.Fatalf("Markdown VGET = %#v, %v", got, err)
	}
	semantic, err := store.Search(ctx, "忘记密码后怎样重新设置登录密码？", scope, 1, 0.5)
	if err != nil || len(semantic) != 1 || semantic[0].Answer.DocumentID != "stage6-password" {
		t.Fatalf("semantic hit = %#v, %v", semantic, err)
	}
	negative, err := store.Search(ctx, "今天北京天气怎么样？", scope, 1, 0.99)
	if err != nil || len(negative) != 0 {
		t.Fatalf("negative query = %#v, %v", negative, err)
	}
	otherScope := scope
	otherScope.Tenant = "other-tenant"
	isolation, err := store.Search(ctx, docs[0].Question, otherScope, 1, -1)
	if err != nil || len(isolation) != 0 {
		t.Fatalf("scope isolation = %#v, %v", isolation, err)
	}
}

func cleanupStage5Probes(ctx context.Context, store Store, scope LogicalScope) error {
	hits, err := store.Search(ctx, "KVLite 阶段五动态验证", scope, 100, -1)
	if err != nil {
		return err
	}
	for _, hit := range hits {
		if strings.HasPrefix(hit.Answer.DocumentID, "stage5-probe-") {
			if err := store.Delete(ctx, hit.Question, scope); err != nil {
				return err
			}
		}
	}
	return nil
}

func getenvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
