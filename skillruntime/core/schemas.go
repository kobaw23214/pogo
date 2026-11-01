package core

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

// schemaFS holds byte-identical copies of docs/skill-runtime-v1/schemas.
// Network schema fetching is forbidden: every reference must resolve inside
// this embedded set.
//
//go:embed schemas/*.schema.json
var schemaFS embed.FS

// schemaIDs maps the document kind (schema base name) to its registered $id.
var schemaIDs = map[string]string{
	KindCommon:             "urn:pogo:skill:1:common",
	KindCapabilityContract: "urn:pogo:skill:1:capability-contract",
	KindSkillRuntime:       "urn:pogo:skill:1:skill-runtime",
	KindAgentBinding:       "urn:pogo:skill:1:agent-binding",
	KindProcessRequest:     "urn:pogo:skill:1:process-request",
	KindProcessResponse:    "urn:pogo:skill:1:process-response",
	KindResolvedLock:       "urn:pogo:skill:1:resolved-lock",
}

// SchemaKinds returns the document kinds accepted by ValidateDocument.
func SchemaKinds() []string {
	kinds := make([]string, 0, len(schemaIDs))
	for k := range schemaIDs {
		kinds = append(kinds, k)
	}
	return kinds
}

// loadSchemaBytes reads an embedded schema file by document kind.
func loadSchemaBytes(kind string) ([]byte, error) {
	if _, ok := schemaIDs[kind]; !ok {
		return nil, fmt.Errorf("unknown document kind %q", kind)
	}
	b, err := schemaFS.ReadFile("schemas/" + kind + ".schema.json")
	if err != nil {
		return nil, fmt.Errorf("read embedded schema %q: %v", kind, err)
	}
	return b, nil
}

// parseSchema unmarshals one schema document.
func parseSchema(kind string) (*jsonschema.Schema, error) {
	b, err := loadSchemaBytes(kind)
	if err != nil {
		return nil, err
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse schema %q: %v", kind, err)
	}
	return &s, nil
}

// offlineLoader resolves only the locally registered urn:pogo:skill:1:*
// schema IDs. Any other URI (http, file, unknown urn) is refused; the
// validator MUST NOT download schemas or follow remote refs.
func offlineLoader(u *url.URL) (*jsonschema.Schema, error) {
	const prefix = "urn:pogo:skill:1:"
	id := u.String()
	if !strings.HasPrefix(id, prefix) {
		return nil, fmt.Errorf("refused non-local schema reference %q", id)
	}
	kind := strings.TrimPrefix(id, prefix)
	if _, ok := schemaIDs[kind]; !ok {
		return nil, fmt.Errorf("unknown local schema reference %q", id)
	}
	return parseSchema(kind)
}

var resolvedCache sync.Map // kind -> *jsonschema.Resolved

// resolvedSchema returns the resolved validator for a document kind.
func resolvedSchema(kind string) (*jsonschema.Resolved, error) {
	if v, ok := resolvedCache.Load(kind); ok {
		return v.(*jsonschema.Resolved), nil
	}
	root, err := parseSchema(kind)
	if err != nil {
		return nil, err
	}
	rs, err := root.Resolve(&jsonschema.ResolveOptions{Loader: offlineLoader})
	if err != nil {
		return nil, fmt.Errorf("resolve schema %q: %v", kind, err)
	}
	resolvedCache.Store(kind, rs)
	return rs, nil
}
