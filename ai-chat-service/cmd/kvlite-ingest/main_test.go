package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviewed.json")
	if err := os.WriteFile(path, []byte(`{"approval_id":"review-42","document_id":"doc-1","question":"如何重置密码","answer":"在登录页点击忘记密码","created_at":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	item, err := loadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if item.DocumentID != "doc-1" {
		t.Fatalf("document_id = %q", item.DocumentID)
	}
}

func TestLoadManifestRejectsUnknownAndIncompleteFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(path, []byte(`{"approval_id":"review-42","document_id":"doc-1","question":"q","answer":"a","created_at":1,"unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadManifest(path); err == nil {
		t.Fatal("expected unknown field error")
	}
}
