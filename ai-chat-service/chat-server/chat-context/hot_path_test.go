package chat_context

import (
	"context"
	"testing"
	"time"
)

func TestHotSessionMetaCacheHit(t *testing.T) {
	store := newSessionStore(newMemoryRawBackend("memory"), "ai:chat").(*sessionStore)
	sessionID := "sess-hot-meta-hit"
	buildCompletedTurn(t, store, sessionID, "msg-1", "pid-0", "hello", "world", 10*time.Second)
	baseline := store.hotPathStats()
	meta1, err := store.LoadSessionMeta(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load session meta: %v", err)
	}
	if meta1 == nil || meta1.SessionID != sessionID {
		t.Fatalf("unexpected meta: %+v", meta1)
	}
	afterFirst := store.hotPathStats()
	meta2, err := store.LoadSessionMeta(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load session meta second time: %v", err)
	}
	if meta2 == nil || meta2.SessionID != sessionID {
		t.Fatalf("unexpected meta on second read: %+v", meta2)
	}
	afterSecond := store.hotPathStats()
	if afterSecond.MetaCacheHit <= afterFirst.MetaCacheHit {
		t.Fatalf("expected meta cache hit to increase: before=%+v after=%+v", afterFirst, afterSecond)
	}
	if afterSecond.MetaCacheHit <= baseline.MetaCacheHit {
		t.Fatalf("expected meta cache hit count to exceed baseline: before=%+v after=%+v", baseline, afterSecond)
	}
}

func TestHotSessionMetaCacheInvalidationAfterWrite(t *testing.T) {
	store := newSessionStore(newMemoryRawBackend("memory"), "ai:chat").(*sessionStore)
	sessionID := "sess-hot-meta-refresh"
	buildCompletedTurn(t, store, sessionID, "msg-1", "pid-0", "hello", "world", 10*time.Second)
	if _, err := store.LoadSessionMeta(context.Background(), sessionID); err != nil {
		t.Fatalf("warm meta cache: %v", err)
	}
	baseline := store.hotPathStats()
	buildCompletedTurn(t, store, sessionID, "msg-2", "assistant-msg-1", "how are you", "fine", 10*time.Second)
	meta, err := store.LoadSessionMeta(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load meta after write: %v", err)
	}
	if meta == nil || meta.LastSeq != 2 || meta.TurnCount != 2 {
		t.Fatalf("meta not refreshed after write: %+v", meta)
	}
	after := store.hotPathStats()
	if after.MetaCacheRefresh <= baseline.MetaCacheRefresh {
		t.Fatalf("expected meta cache refresh after write: before=%+v after=%+v", baseline, after)
	}
}

func TestRecentWindowCacheHit(t *testing.T) {
	store := newSessionStore(newMemoryRawBackend("memory"), "ai:chat").(*sessionStore)
	sessionID := "sess-hot-window-hit"
	turn1 := buildCompletedTurn(t, store, sessionID, "msg-1", "pid-0", "hello", "world", 10*time.Second)
	turn2 := buildCompletedTurn(t, store, sessionID, "msg-2", "assistant-msg-1", "how are you", "fine", 10*time.Second)
	first, err := store.LoadRecentTurns(context.Background(), sessionID, 2)
	if err != nil {
		t.Fatalf("load recent turns first: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("unexpected first turn count: %+v", first)
	}
	if first[0].Seq != turn1.Seq || first[1].Seq != turn2.Seq {
		t.Fatalf("unexpected turn order on first read: %+v", first)
	}
	afterFirst := store.hotPathStats()
	second, err := store.LoadRecentTurns(context.Background(), sessionID, 2)
	if err != nil {
		t.Fatalf("load recent turns second: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("unexpected second turn count: %+v", second)
	}
	if second[0].Seq != turn1.Seq || second[1].Seq != turn2.Seq {
		t.Fatalf("unexpected turn order on second read: %+v", second)
	}
	afterSecond := store.hotPathStats()
	if afterSecond.RecentWindowCacheHit <= afterFirst.RecentWindowCacheHit {
		t.Fatalf("expected recent window cache hit to increase: before=%+v after=%+v", afterFirst, afterSecond)
	}
}

func TestRecentWindowCacheInvalidationOnSeqAdvance(t *testing.T) {
	store := newSessionStore(newMemoryRawBackend("memory"), "ai:chat").(*sessionStore)
	sessionID := "sess-hot-window-refresh"
	turn1 := buildCompletedTurn(t, store, sessionID, "msg-1", "pid-0", "hello", "world", 10*time.Second)
	turn2 := buildCompletedTurn(t, store, sessionID, "msg-2", "assistant-msg-1", "how are you", "fine", 10*time.Second)
	if _, err := store.LoadRecentTurns(context.Background(), sessionID, 2); err != nil {
		t.Fatalf("warm recent window cache: %v", err)
	}
	baseline := store.hotPathStats()
	turn3 := buildCompletedTurn(t, store, sessionID, "msg-3", "assistant-msg-2", "new question", "new answer", 10*time.Second)
	turns, err := store.LoadRecentTurns(context.Background(), sessionID, 2)
	if err != nil {
		t.Fatalf("load recent turns after seq advance: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("unexpected turn count after seq advance: %+v", turns)
	}
	if turns[0].Seq != turn2.Seq || turns[1].Seq != turn3.Seq {
		t.Fatalf("window did not advance correctly: %+v", turns)
	}
	if turns[0].Seq == turn1.Seq {
		t.Fatalf("expected stale window to be replaced: %+v", turns)
	}
	after := store.hotPathStats()
	if after.RecentWindowCacheRefresh <= baseline.RecentWindowCacheRefresh {
		t.Fatalf("expected recent window cache refresh after seq advance: before=%+v after=%+v", baseline, after)
	}
}

func TestHotPathStillReturnsCorrectTurnOrder(t *testing.T) {
	store := newSessionStore(newMemoryRawBackend("memory"), "ai:chat").(*sessionStore)
	sessionID := "sess-hot-order"
	turn1 := buildCompletedTurn(t, store, sessionID, "msg-1", "pid-0", "hello", "world", 10*time.Second)
	turn2 := buildCompletedTurn(t, store, sessionID, "msg-2", "assistant-msg-1", "how are you", "fine", 10*time.Second)
	turn3 := buildCompletedTurn(t, store, sessionID, "msg-3", "assistant-msg-2", "new question", "new answer", 10*time.Second)
	turns, err := store.LoadRecentTurns(context.Background(), sessionID, 3)
	if err != nil {
		t.Fatalf("load recent turns: %v", err)
	}
	if len(turns) != 3 {
		t.Fatalf("unexpected turn count: %+v", turns)
	}
	if turns[0].Seq != turn1.Seq || turns[1].Seq != turn2.Seq || turns[2].Seq != turn3.Seq {
		t.Fatalf("turn order mismatch: %+v", turns)
	}
	if turns[0].UserMessage.Content != "hello" || turns[2].AssistantMessage.Content != "new answer" {
		t.Fatalf("unexpected turn content: %+v", turns)
	}
}

func TestHotPathDoesNotBreakSessionConsistency(t *testing.T) {
	store := newSessionStore(newMemoryRawBackend("memory"), "ai:chat").(*sessionStore)
	sessionA := "sess-hot-consistency-a"
	sessionB := "sess-hot-consistency-b"
	turnA1 := buildCompletedTurn(t, store, sessionA, "a-msg-1", "a-pid-0", "hello a1", "world a1", 10*time.Second)
	turnA2 := buildCompletedTurn(t, store, sessionA, "a-msg-2", "assistant-a-msg-1", "hello a2", "world a2", 10*time.Second)
	turnB1 := buildCompletedTurn(t, store, sessionB, "b-msg-1", "b-pid-0", "hello b1", "world b1", 10*time.Second)
	turnB2 := buildCompletedTurn(t, store, sessionB, "b-msg-2", "assistant-b-msg-1", "hello b2", "world b2", 10*time.Second)

	metaA, err := store.LoadSessionMeta(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("load session A meta: %v", err)
	}
	if metaA == nil || metaA.SessionID != sessionA || metaA.LastSeq != turnA2.Seq {
		t.Fatalf("unexpected session A meta: %+v", metaA)
	}
	turnsA, err := store.LoadRecentTurns(context.Background(), sessionA, 2)
	if err != nil {
		t.Fatalf("load session A turns: %v", err)
	}
	if len(turnsA) != 2 || turnsA[0].SessionID != sessionA || turnsA[1].SessionID != sessionA {
		t.Fatalf("session A turn leak: %+v", turnsA)
	}

	metaB, err := store.LoadSessionMeta(context.Background(), sessionB)
	if err != nil {
		t.Fatalf("load session B meta: %v", err)
	}
	if metaB == nil || metaB.SessionID != sessionB || metaB.LastSeq != turnB2.Seq {
		t.Fatalf("unexpected session B meta: %+v", metaB)
	}
	turnsB, err := store.LoadRecentTurns(context.Background(), sessionB, 2)
	if err != nil {
		t.Fatalf("load session B turns: %v", err)
	}
	if len(turnsB) != 2 || turnsB[0].SessionID != sessionB || turnsB[1].SessionID != sessionB {
		t.Fatalf("session B turn leak: %+v", turnsB)
	}

	if turnA1.SessionID == turnB1.SessionID {
		t.Fatalf("test setup error: sessions must differ")
	}
	if turnsA[0].Seq != turnA1.Seq || turnsA[1].Seq != turnA2.Seq {
		t.Fatalf("session A seq mismatch: %+v", turnsA)
	}
	if turnsB[0].Seq != turnB1.Seq || turnsB[1].Seq != turnB2.Seq {
		t.Fatalf("session B seq mismatch: %+v", turnsB)
	}
}
