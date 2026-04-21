package main

import (
	"math"
	"testing"
)

func TestBuildEmbeddingVectorDifferentiatesChineseQuestions(t *testing.T) {
	left := buildEmbeddingVector("C++ 有哪些需要重点学习的内容")
	right := buildEmbeddingVector("如何学习 C++")
	repeat := buildEmbeddingVector("C++ 有哪些需要重点学习的内容")

	if len(left) != len(right) || len(left) != len(repeat) {
		t.Fatalf("unexpected vector dimensions: left=%d right=%d repeat=%d", len(left), len(right), len(repeat))
	}
	if !vectorsAlmostEqual(left, repeat) {
		t.Fatalf("expected deterministic vectors for the same text")
	}
	if vectorsAlmostEqual(left, right) {
		t.Fatalf("expected different Chinese questions to produce different vectors")
	}
	if cosineSimilarity(left, right) >= 0.9999 {
		t.Fatalf("expected cosine similarity below 1.0, got %f", cosineSimilarity(left, right))
	}
}

func vectorsAlmostEqual(left, right []float32) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if math.Abs(float64(left[index]-right[index])) > 1e-6 {
			return false
		}
	}
	return true
}

func cosineSimilarity(left, right []float32) float64 {
	var dot, leftNorm, rightNorm float64
	for index := range left {
		dot += float64(left[index] * right[index])
		leftNorm += float64(left[index] * left[index])
		rightNorm += float64(right[index] * right[index])
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}
