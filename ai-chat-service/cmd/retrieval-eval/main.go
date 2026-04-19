package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ai-chat-service/chat-server/qa"
	"ai-chat-service/pkg/config"
)

func main() {
	defaultDataset := filepath.FromSlash("chat-server/qa/testdata/retrieval_eval_suite.v1.json")
	defaultOutputDir := filepath.FromSlash("artifacts/benchmarks/semantic_vs_lexical")
	defaultConfig := filepath.FromSlash("dev.config.yaml")

	var datasetPath string
	var outputDir string
	var configPath string
	var embeddingMode string
	var embeddingProfile string
	var semanticMode string
	flag.StringVar(&datasetPath, "dataset", defaultDataset, "path to the retrieval eval suite JSON file")
	flag.StringVar(&outputDir, "output-dir", defaultOutputDir, "directory for report.json and report.md")
	flag.StringVar(&configPath, "config", defaultConfig, "path to the ai-chat-service config file")
	flag.StringVar(&embeddingMode, "embedding-mode", string(qa.RetrievalEvalEmbeddingModePlaceholder), "embedding mode: placeholder or provider")
	flag.StringVar(&embeddingProfile, "embedding-profile", "", "embedding provider profile name to use in provider mode")
	flag.StringVar(&semanticMode, "semantic-mode", string(qa.SemanticRecallModeBruteForce), "semantic recall mode: brute_force or ann")
	flag.Parse()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[retrieval-eval] load config: %v\n", err)
		os.Exit(1)
	}
	embeddingSource, err := qa.ResolveRetrievalEvalEmbeddingSource(cfg, embeddingMode, embeddingProfile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[retrieval-eval] resolve embedding source: %v\n", err)
		os.Exit(1)
	}

	suite, err := qa.LoadRetrievalEvalSuite(datasetPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[retrieval-eval] load dataset: %v\n", err)
		os.Exit(1)
	}
	report, err := qa.EvaluateRetrievalSuiteWithOptions(context.Background(), suite, datasetPath, qa.RetrievalEvalOptions{EmbeddingSource: embeddingSource, SemanticMode: semanticMode})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[retrieval-eval] evaluate suite: %v\n", err)
		os.Exit(1)
	}

	if err := report.WriteJSON(filepath.Join(outputDir, "report.json")); err != nil {
		fmt.Fprintf(os.Stderr, "[retrieval-eval] write json report: %v\n", err)
		os.Exit(1)
	}
	if err := report.WriteMarkdown(filepath.Join(outputDir, "report.md")); err != nil {
		fmt.Fprintf(os.Stderr, "[retrieval-eval] write markdown report: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(renderRetrievalEvalStdout(report, outputDir))
}

func renderRetrievalEvalStdout(report *qa.RetrievalEvalReport, outputDir string) string {
	if report == nil {
		return "[retrieval-eval] report is nil\n"
	}
	var builder strings.Builder
	builder.WriteString(report.RenderText())
	builder.WriteString("\n[retrieval-eval] markdown report: ")
	builder.WriteString(filepath.Join(outputDir, "report.md"))
	builder.WriteString("\n[retrieval-eval] json report: ")
	builder.WriteString(filepath.Join(outputDir, "report.json"))
	builder.WriteString("\n")
	return builder.String()
}
