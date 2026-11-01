package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDescribeContractAndRejectWrongOperation(t *testing.T) {
	var out bytes.Buffer
	if err := serve(strings.NewReader(`{"protocolVersion":"pogo.tool/1","callId":"call:test","type":"describe"}`), &out); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	ops := got["operations"].([]any)
	if len(ops) != 1 || ops[0].(map[string]any)["contract"].(map[string]any)["digest"] != contractIdentity().Digest {
		t.Fatal("describe contract mismatch")
	}
	out.Reset()
	if err := serve(strings.NewReader(`{"protocolVersion":"pogo.tool/1","callId":"call:test","type":"invoke","operation":"run.shell"}`), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "UNSUPPORTED_OPERATION") {
		t.Fatal(out.String())
	}
}

func TestParseYARAMatches(t *testing.T) {
	got, err := parseMatches("default:PrototypeMarker /input/0.bin\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Rule != "PrototypeMarker" || got[0].Namespace != "default" {
		t.Fatalf("%+v", got)
	}
	got, err = parseMatches("")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatal("empty successful scan must return []")
	}
	if _, err = parseMatches("unparseable diagnostic"); err == nil {
		t.Fatal("diagnostic accepted as match")
	}
}
