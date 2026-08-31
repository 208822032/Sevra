// kvlite-ingest writes one reviewed knowledge document into the KVLite vector
// backend configured for AI-chat. It deliberately has no automatic discovery
// or chat-answer input path: every write needs an explicit manifest and an
// approval identifier.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"ai-chat-service/chat-server/retrieval/kvlite"
	"ai-chat-service/pkg/config"
)

type manifest struct {
	ApprovalID string `json:"approval_id"`
	DocumentID string `json:"document_id"`
	Question   string `json:"question"`
	Answer     string `json:"answer"`
	CreatedAt  int64  `json:"created_at"`
}

func main() {
	configPath := flag.String("config", "dev.config.yaml", "AI-chat configuration path")
	inputPath := flag.String("input", "", "reviewed knowledge JSON manifest path")
	flag.Parse()
	if strings.TrimSpace(*inputPath) == "" {
		log.Fatal("-input is required")
	}

	cnf, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	if !cnf.Retrieval.KVLite.Enabled {
		log.Fatal("retrieval.kvlite.enabled must be true")
	}
	item, err := loadManifest(*inputPath)
	if err != nil {
		log.Fatal(err)
	}

	retrieval := cnf.Retrieval.KVLite
	clientConfig := kvlite.DefaultConfig(retrieval.Address)
	timeout := time.Duration(retrieval.TimeoutMS) * time.Millisecond
	clientConfig.ConnectTimeout = timeout
	clientConfig.ReadTimeout = timeout
	clientConfig.WriteTimeout = timeout
	clientConfig.PoolSize = retrieval.PoolSize
	clientConfig.MaxIdle = retrieval.MaxIdle
	store, err := kvlite.NewStore(clientConfig)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := store.Health(ctx); err != nil {
		log.Fatalf("KVLite vector health check failed: %v", err)
	}
	writer, err := kvlite.NewControlledWriter(store)
	if err != nil {
		log.Fatal(err)
	}
	document := kvlite.Document{
		Question: item.Question,
		Scope: kvlite.LogicalScope{
			Tenant:        retrieval.Scope.Tenant,
			KnowledgeBase: retrieval.Scope.KnowledgeBase,
			CorpusVersion: retrieval.Scope.CorpusVersion,
			Provider:      retrieval.Scope.Provider,
			Model:         retrieval.Scope.Model,
			PromptVersion: retrieval.Scope.PromptVersion,
		},
		Answer: kvlite.AnswerEnvelope{
			Version:    kvlite.AnswerEnvelopeVersion,
			DocumentID: item.DocumentID,
			Answer:     item.Answer,
			Source:     "knowledge_base",
			CreatedAt:  item.CreatedAt,
		},
	}
	if err := writer.UpsertApproved(ctx, kvlite.ApprovedDocument{Document: document, ApprovalID: item.ApprovalID}); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("ingested document_id=%s into tenant=%s knowledge_base=%s corpus_version=%s\n", item.DocumentID, document.Scope.Tenant, document.Scope.KnowledgeBase, document.Scope.CorpusVersion)
}

func loadManifest(path string) (manifest, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var item manifest
	if err := decoder.Decode(&item); err != nil {
		return manifest{}, fmt.Errorf("decode ingestion manifest: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return manifest{}, err
	}
	if strings.TrimSpace(item.ApprovalID) == "" || strings.TrimSpace(item.DocumentID) == "" || strings.TrimSpace(item.Question) == "" || strings.TrimSpace(item.Answer) == "" || item.CreatedAt <= 0 {
		return manifest{}, fmt.Errorf("ingestion manifest requires approval_id, document_id, question, answer and positive created_at")
	}
	return item, nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("ingestion manifest contains multiple JSON values")
		}
		return fmt.Errorf("read ingestion manifest tail: %w", err)
	}
	return nil
}
