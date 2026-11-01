package skillruntime

import "testing"

func TestMatchArtifactsRequiresExactDeclaredSet(t *testing.T) {
	collected := []CollectedArtifact{{Path: "reports/a.bin", Data: []byte("aaa")}, {Path: "b.txt", Data: []byte("bb")}}
	declared := []DeclaredArtifact{{Name: "a", Path: "./reports/a.bin", MediaType: "application/octet-stream"}, {Name: "b", Path: "./b.txt", MediaType: "text/plain"}}
	arts, blobs, err := matchArtifacts(declared, collected)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 2 || len(blobs) != 2 {
		t.Fatalf("unexpected match: %+v", arts)
	}
	if !artifactIDPattern.MatchString(arts[0].ArtifactID) || arts[0].Digest != bytesDigest([]byte("aaa")) || arts[0].SizeBytes != 3 || arts[1].MediaType != "text/plain" {
		t.Fatalf("bad artifact metadata: %+v", arts)
	}
	// Undeclared collected file fails the whole call.
	if _, _, err := matchArtifacts(declared[:1], collected); err == nil {
		t.Fatal("undeclared artifact accepted")
	}
	// Declared but never written fails the whole call.
	if _, _, err := matchArtifacts(declared, collected[:1]); err == nil {
		t.Fatal("missing artifact accepted")
	}
	// Duplicate declarations fail.
	if _, _, err := matchArtifacts([]DeclaredArtifact{declared[0], declared[0]}, collected[:1]); err == nil {
		t.Fatal("duplicate declaration accepted")
	}
	// Unsafe declared paths fail.
	if _, _, err := matchArtifacts([]DeclaredArtifact{{Name: "x", Path: "../evil", MediaType: "text/plain"}}, nil); err == nil {
		t.Fatal("unsafe declared path accepted")
	}
	// Oversize payloads fail even if the executor check was bypassed.
	if _, _, err := matchArtifacts(declared[:1], []CollectedArtifact{{Path: "reports/a.bin", Data: make([]byte, maxArtifactFileBytes+1)}}); err == nil {
		t.Fatal("oversize matched artifact accepted")
	}
	// No declaration and no files is a valid empty artifact call.
	if arts, blobs, err := matchArtifacts(nil, nil); err != nil || arts != nil || blobs != nil {
		t.Fatalf("empty artifact call rejected: %v", err)
	}
}
