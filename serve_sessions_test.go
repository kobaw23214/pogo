package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServePreservesEmptyTranscriptsDuringMaintenance(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "empty.jsonl")
	data := []byte("{\"type\":\"meta\",\"meta\":{}}\n")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "release-maintenance.json")
	if err := os.WriteFile(marker, []byte("maintenance boundary"), 0600); err != nil {
		t.Fatal(err)
	}
	if count, err := cleanEmptySessionsForServe(dir); err != nil || count != 0 {
		t.Fatalf("maintenance cleanup = %d, %v", count, err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != string(data) {
		t.Fatalf("transcript changed during maintenance: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if count, err := cleanEmptySessionsForServe(dir); err != nil || count != 1 {
		t.Fatalf("normal cleanup = %d, %v", count, err)
	}
}
