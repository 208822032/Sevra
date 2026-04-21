package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ai-chat-service/chat-server/qa"
)

func main() {
	defaultInput := filepath.FromSlash("artifacts/benchmarks/semantic_ann_samples.jsonl")
	var inputPath string
	var topN int
	flag.StringVar(&inputPath, "input", defaultInput, "path to exported ANN samples in JSONL format")
	flag.IntVar(&topN, "top-n", 5, "number of top queries to show per reason")
	flag.Parse()

	if strings.TrimSpace(inputPath) == "" {
		fmt.Fprintln(os.Stderr, "[semantic-ann-eval] input path is required")
		os.Exit(1)
	}

	samples, err := qa.LoadSemanticANNSamples(inputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[semantic-ann-eval] load samples: %v\n", err)
		os.Exit(1)
	}
	report := qa.AnalyzeSemanticANNSamples(samples, topN)
	fmt.Print(report.RenderText())
}
