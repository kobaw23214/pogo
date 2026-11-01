package web

// Workflow definitions: chains of Studio agents. The store clones the Agent
// Studio registry's guarantees — one JSON file, atomic writes, strict decode,
// .initialized refuse-to-boot, revision optimistic locking — with the graph
// validation delegated to the workflow package's engine.
import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/tools"
	"github.com/yosukeno/pogo/workflow"
)

const workflowFormatVersion = 1

// maxWorkflows bounds the registry, same role as the Agent cap.
const maxWorkflows = 100

type workflowInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Status is retired: the publish model replaced the ready gate. The field
	// stays so registries written before the model existed still decode (the
	// strict loader would otherwise refuse them), and migration reads it once.
	Status string          `json:"status,omitempty"`
	Nodes  []workflow.Node `json:"nodes"`
}

type Workflow struct {
	workflowInput
	ID        string `json:"id"`
	Revision  int    `json:"revision"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Deleted   bool   `json:"deleted,omitempty"`
}

// definition projects the stored record into the engine's view, which is also
// the snapshot a run freezes into its record.
func (w Workflow) definition() workflow.Definition {
	return workflow.Definition{
		ID:          w.ID,
		Name:        w.Name,
		Description: w.Description,
		Revision:    w.Revision,
		Nodes:       append([]workflow.Node(nil), w.Nodes...),
	}
}

type workflowData struct {
	Version           int                                 `json:"version"`
	Workflows         map[string]Workflow                 `json:"workflows"`
	AuthoringReceipts map[string]workflowAuthoringReceipt `json:"authoring_receipts,omitempty"`
}

type workflowStore struct {
	mu   sync.RWMutex
	path string
	data workflowData
	// pubPath/versionsDir/published are the publish layer (workflowpublish.go),
	// guarded by the same mu as the registry.
	pubPath     string
	versionsDir string
	published   publishedData
	// refs validates node agent/model/tool references against the manager's
	// live catalogs. NewManager installs it; a store built bare in a test
	// leaves it nil and accepts the shape-only validation.
	refs func(*workflowInput) error
}

func openWorkflowStore(dir string) (*workflowStore, error) {
	path := filepath.Join(dir, "workflows.json")
	s := &workflowStore{path: path,
		data: workflowData{Version: workflowFormatVersion, Workflows: map[string]Workflow{}}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		if _, markerErr := os.Stat(path + ".initialized"); markerErr == nil {
			return nil, errors.New("workflow registry is missing from an initialized data directory")
		} else if !errors.Is(markerErr, os.ErrNotExist) {
			return nil, markerErr
		}
		if err := s.save(s.data); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		raw, readErr := io.ReadAll(io.LimitReader(f, (32<<20)+1))
		closeErr := f.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(raw) > 32<<20 {
			return nil, errors.New("workflow registry exceeds size limit")
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s.data); err != nil {
			return nil, fmt.Errorf("workflow registry: %w", err)
		}
		var trailing any
		if err := dec.Decode(&trailing); err != io.EOF {
			return nil, errors.New("workflow registry has trailing data")
		}
		if s.data.Version != workflowFormatVersion || s.data.Workflows == nil {
			return nil, errors.New("invalid workflow registry")
		}
		for id, w := range s.data.Workflows {
			if !validID(id) || id != w.ID || w.Revision < 1 {
				return nil, errors.New("invalid workflow definition")
			}
			if err := w.definition().ValidateShape(); err != nil {
				return nil, fmt.Errorf("invalid workflow definition: %w", err)
			}
		}
	}
	if err := os.WriteFile(path+".initialized", []byte("workflows-v1\n"), 0600); err != nil {
		return nil, err
	}
	if err := s.openPublished(dir); err != nil {
		return nil, err
	}
	if err := s.migratePublished(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *workflowStore) save(next workflowData) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > 32<<20 {
		return errors.New("workflow registry capacity reached")
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".workflows-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *workflowStore) get(id string) (Workflow, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.data.Workflows[id]
	return w, ok && !w.Deleted
}

func (s *workflowStore) list(admin bool) []Workflow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Workflow{}
	for _, w := range s.data.Workflows {
		if w.Deleted {
			continue
		}
		if !admin {
			for i := range w.Nodes {
				w.Nodes[i].Task = ""
				w.Nodes[i].Prompt = ""
			}
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out
}

func validateWorkflowInput(in *workflowInput) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 60 {
		return errors.New("名称需要 1–60 个字符")
	}
	if utf8.RuneCountInString(in.Description) > 240 {
		return errors.New("描述超过长度限制")
	}
	// Status is retired input: accepted from old clients, carried nowhere.
	in.Status = ""
	def := workflow.Definition{ID: "wf_draft", Name: in.Name, Revision: 1, Nodes: in.Nodes}
	def.Normalize()
	in.Nodes = def.Nodes
	if err := def.ValidateShape(); err != nil {
		return fmt.Errorf("无效的工作流定义：%w", err)
	}
	for i := range in.Nodes {
		n := &in.Nodes[i]
		if n.Tools != nil {
			ids, err := normalizeIDList(*n.Tools, "内置工具")
			if err != nil {
				return err
			}
			n.Tools = &ids
		}
		if n.MaxTurns > 100 {
			return fmt.Errorf("节点 %s 的轮次上限不能超过 100", n.ID)
		}
	}
	return nil
}

func (s *workflowStore) put(id string, input workflowInput, expected int) (Workflow, error) {
	if err := validateWorkflowInput(&input); err != nil {
		return Workflow{}, err
	}
	if s.refs != nil {
		if err := s.refs(&input); err != nil {
			return Workflow{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	w := Workflow{workflowInput: input, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if id != "" {
		old, ok := s.data.Workflows[id]
		if !ok || old.Deleted {
			return w, ErrNotFound
		}
		if expected != old.Revision {
			return w, errors.New("工作流已被更新，请刷新后重试")
		}
		w.ID, w.CreatedAt, w.Revision = id, old.CreatedAt, old.Revision+1
	} else {
		if len(s.data.Workflows) >= maxWorkflows {
			return w, errors.New("工作流数量已达到当前上限")
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return w, err
		}
		w.ID = "wf_" + hex.EncodeToString(nonce[:])
	}
	next := s.data
	next.Workflows = maps.Clone(s.data.Workflows)
	next.Workflows[w.ID] = w
	if err := s.save(next); err != nil {
		return w, err
	}
	s.data = next
	return w, nil
}

func (s *workflowStore) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.data.Workflows[id]
	if !ok || w.Deleted {
		return ErrNotFound
	}
	w.Deleted, w.UpdatedAt = true, time.Now().UTC().Format(time.RFC3339Nano)
	next := s.data
	next.Workflows = maps.Clone(s.data.Workflows)
	next.Workflows[id] = w
	if err := s.save(next); err != nil {
		return err
	}
	s.data = next
	return s.unpublishLocked(id)
}

// validateWorkflowRefs is the workflow store's reference hook: every node's
// agent must be a live definition, and a node's model or tool narrowing must
// name things that exist — the same fail-closed rule the Studio definitions
// follow. Deleted or unknown agents are rejected; a draft agent is fine, the
// run start is where readiness is enforced.
func (m *Manager) validateWorkflowRefs(in *workflowInput) error {
	checkAgent := func(id string) error {
		if _, ok := m.studio.get(id); !ok {
			return fmt.Errorf("引用的 Agent 不存在或已删除：%s", id)
		}
		return nil
	}
	for _, n := range in.Nodes {
		if n.Kind != workflow.KindHuman {
			if err := checkAgent(n.Agent); err != nil {
				return fmt.Errorf("节点 %s %w", n.ID, err)
			}
		}
		if n.Orchestrate != nil {
			for _, c := range n.Orchestrate.Candidates {
				if err := checkAgent(c.Agent); err != nil {
					return fmt.Errorf("节点 %s 的候选 %w", n.ID, err)
				}
			}
		}
		if n.Model != "" {
			if _, err := config.Resolve(n.Model); err != nil {
				return err
			}
		}
		if n.Tools != nil {
			known := tools.BuiltinNameSet()
			for _, name := range *n.Tools {
				if !known[name] {
					return fmt.Errorf("未知的内置工具：%s", name)
				}
			}
		}
	}
	return nil
}
