package session

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

func TestReadTranscriptUsesOpenSemanticsWithoutSourceWrites(t *testing.T) {
	original, err := Create(t.TempDir(), "/synthetic", "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if err = original.Append(llm.UserText("retained historical message")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(original.Path())
	if err != nil {
		t.Fatal(err)
	}
	fromPath, err := Open(original.Path())
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := Read(bytes.NewReader(before))
	if err != nil {
		t.Fatal(err)
	}
	if readOnly.Path() != "" || !reflect.DeepEqual(fromPath.BranchRecords(""), readOnly.BranchRecords("")) {
		t.Fatal("reader changed committed branch semantics")
	}
	if err = readOnly.Append(llm.UserText("must not persist")); err == nil {
		t.Fatal("reader-only store appended to a source")
	}
	after, err := os.ReadFile(original.Path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("read-only inspection modified source: %v", err)
	}
}
