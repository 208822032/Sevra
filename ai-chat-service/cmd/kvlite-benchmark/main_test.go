package main

import (
	"testing"
	"time"
)

func TestSelectCasesUsesAnswerAsExpectedDocument(t *testing.T) {
	docs, cases, err := selectCases([]fixtureRow{
		{Question: "怎么重置密码", Answer: "去登录页"},
		{Question: "忘记密码怎么办", Answer: "去登录页"},
		{Question: "怎么改手机", Answer: "账号安全"},
	}, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || len(cases) != 3 || cases[1].expectedID != docs[0].id {
		t.Fatalf("docs=%+v cases=%+v", docs, cases)
	}
}

func TestSummarizeNearestRank(t *testing.T) {
	got := summarize([]time.Duration{4 * time.Microsecond, time.Microsecond, 2 * time.Microsecond, 3 * time.Microsecond})
	if got.P50US != 2 || got.P95US != 4 || got.P99US != 4 {
		t.Fatalf("got %+v", got)
	}
}
