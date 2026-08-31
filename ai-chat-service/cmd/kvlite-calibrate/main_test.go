package main

import (
	"testing"

	"ai-chat-service/chat-server/retrieval/kvlite"
)

func TestEvaluateRecommendsLowestSafeThreshold(t *testing.T) {
	cases := []retrievedCase{
		{qaCase: qaCase{ID: "p1", Query: "q", ExpectedDocumentID: "d1"}, Hits: []kvlite.Hit{{Document: kvlite.Document{Answer: kvlite.AnswerEnvelope{DocumentID: "d1"}}, Score: .91}}},
		{qaCase: qaCase{ID: "n1", Query: "q"}, Hits: []kvlite.Hit{{Document: kvlite.Document{Answer: kvlite.AnswerEnvelope{DocumentID: "wrong"}}, Score: .85}}},
	}
	result := evaluate(cases, []int{1}, []float64{.80, .90}, 2, .99, 0)
	if result.Recommendation == nil || *result.Recommendation != .90 {
		t.Fatalf("recommendation = %#v", result.Recommendation)
	}
	if result.TopK[0].RecallAtK != 1 {
		t.Fatalf("recall = %v", result.TopK[0].RecallAtK)
	}
}

func TestParseLists(t *testing.T) {
	thresholds, err := parseFloatList("0.9,0.8", -1, 1)
	if err != nil || len(thresholds) != 2 || thresholds[0] != .8 {
		t.Fatalf("thresholds=%v err=%v", thresholds, err)
	}
	if _, err := parseIntList("0", 1, 1024); err == nil {
		t.Fatal("expected invalid top-k")
	}
}
