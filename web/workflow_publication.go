package web

// Publication is a deliberately separate layer from the legacy published
// pointer. Existing workflows and runs do not change when an embed is added.
import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
)

type workflowPublicationInput struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
}
type workflowPublicationMapping struct {
	Subject  string `json:"subject"`
	Username string `json:"username"`
}
type workflowPublicationCallback struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
}

type workflowPublication struct {
	ID             string                       `json:"id"`
	WorkflowID     string                       `json:"workflow_id"`
	VersionID      string                       `json:"version_id"`
	Name           string                       `json:"name"`
	HistoryScope   string                       `json:"history_scope"`
	Callback       *workflowPublicationCallback `json:"callback,omitempty"`
	SigningSecret  string                       `json:"-"`
	Enabled        bool                         `json:"enabled"`
	AllowedOrigins []string                     `json:"allowed_origins"`
	Revision       int                          `json:"revision"`
	InputFields    []workflowPublicationInput   `json:"input_fields"`
	Mappings       []workflowPublicationMapping `json:"mappings"`
	SecretHash     string                       `json:"-"`
}

// The private envelope never crosses HTTP. Reads expose no secret hash.
type workflowPublicationStored struct {
	workflowPublication
	SecretHash            string `json:"secret_hash"`
	CallbackSigningSecret string `json:"callback_signing_secret,omitempty"`
}
type workflowPublicationData struct {
	Version      int                                  `json:"version"`
	Publications map[string]workflowPublicationStored `json:"publications"`
}
type workflowPublicationStore struct {
	mu   sync.RWMutex
	path string
	data workflowPublicationData
}

func openWorkflowPublications(dir string) (*workflowPublicationStore, error) {
	s := &workflowPublicationStore{path: filepath.Join(dir, "workflows", "publications.json"), data: workflowPublicationData{Version: 1, Publications: map[string]workflowPublicationStored{}}}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 8<<20 {
		return nil, errors.New("workflow publications exceed size limit")
	}
	if err = json.Unmarshal(raw, &s.data); err != nil {
		return nil, err
	}
	if s.data.Version != 1 || s.data.Publications == nil {
		return nil, errors.New("unsupported workflow publication registry")
	}
	for id, stored := range s.data.Publications {
		p := stored.workflowPublication
		p.SecretHash = stored.SecretHash
		p.SigningSecret = stored.CallbackSigningSecret
		if p.HistoryScope == "" {
			p.HistoryScope = "workflow"
		}
		if p.Callback != nil && p.Callback.Enabled && p.SigningSecret == "" {
			return nil, errors.New("callback signing secret missing")
		}
		if id != p.ID || !validID(p.ID) || !validID(p.WorkflowID) || !validID(p.VersionID) || len(p.SecretHash) != 64 || p.Revision < 1 {
			return nil, errors.New("invalid workflow publication registry")
		}
		if _, err := hex.DecodeString(p.SecretHash); err != nil {
			return nil, errors.New("invalid workflow publication secret hash")
		}
		if err := validateWorkflowPublication(&p); err != nil {
			return nil, fmt.Errorf("invalid workflow publication: %w", err)
		}
		s.data.Publications[id] = workflowPublicationStored{workflowPublication: p, SecretHash: p.SecretHash, CallbackSigningSecret: p.SigningSecret}
	}
	return s, nil
}
func cloneWorkflowPublication(p workflowPublication) workflowPublication {
	if p.Callback != nil {
		callback := *p.Callback
		p.Callback = &callback
	}
	p.AllowedOrigins = slices.Clone(p.AllowedOrigins)
	p.InputFields = slices.Clone(p.InputFields)
	p.Mappings = slices.Clone(p.Mappings)
	return p
}
func (s *workflowPublicationStore) get(id string) (workflowPublication, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stored, ok := s.data.Publications[id]
	p := cloneWorkflowPublication(stored.workflowPublication)
	p.SecretHash = stored.SecretHash
	p.SigningSecret = stored.CallbackSigningSecret
	return p, ok
}
func (s *workflowPublicationStore) list(wid string) []workflowPublication {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []workflowPublication{}
	for _, stored := range s.data.Publications {
		if stored.WorkflowID == wid {
			p := cloneWorkflowPublication(stored.workflowPublication)
			p.SecretHash = stored.SecretHash
			p.SigningSecret = stored.CallbackSigningSecret
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (s *workflowPublicationStore) save(p workflowPublication, expected int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.data.Publications[p.ID]
	if exists && old.Revision != expected || !exists && expected != 0 {
		return errors.New("发布入口已被其他操作修改，请刷新后重试")
	}
	next := workflowPublicationData{Version: 1, Publications: make(map[string]workflowPublicationStored, len(s.data.Publications)+1)}
	for id, v := range s.data.Publications {
		next.Publications[id] = v
	}
	p.Revision = expected + 1
	next.Publications[p.ID] = workflowPublicationStored{workflowPublication: cloneWorkflowPublication(p), SecretHash: p.SecretHash, CallbackSigningSecret: p.SigningSecret}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err = writeWorkflowFileAtomic(s.path, raw); err != nil {
		return err
	}
	s.data = next
	return nil
}

func workflowClientSecret() (string, string) {
	raw := "wic_" + rand.Text() + rand.Text()
	sum := sha256.Sum256([]byte(raw))
	return raw, hex.EncodeToString(sum[:])
}

func normalizeWorkflowEmbedOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(raw, "* \t\r\n;") {
		return "", errors.New("嵌入来源须为完整的网站 origin，不支持通配符或路径")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !(strings.EqualFold(u.Hostname(), "localhost") || ip != nil && ip.IsLoopback()) {
			return "", errors.New("嵌入来源须使用 HTTPS；本机开发可使用 HTTP")
		}
	}
	if u.Port() != "" {
		if _, err := net.LookupPort("tcp", u.Port()); err != nil {
			return "", errors.New("嵌入来源端口无效")
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = ""
	u.RawPath = ""
	if u.Scheme == "https" && u.Port() == "443" || u.Scheme == "http" && u.Port() == "80" {
		u.Host = u.Hostname()
		if strings.Contains(u.Host, ":") {
			u.Host = "[" + u.Host + "]"
		}
	}
	return u.String(), nil
}

var workflowInputKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

func validateWorkflowPublication(p *workflowPublication) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.HistoryScope == "" {
		p.HistoryScope = "business"
	}
	if p.HistoryScope != "business" && p.HistoryScope != "workflow" {
		return errors.New("运行记录范围无效")
	}
	if p.Callback != nil {
		if err := validateWorkflowCallbackURL(p.Callback.URL, p.Callback.Enabled); err != nil {
			return err
		}
	}
	if p.Name == "" || len([]rune(p.Name)) > 100 || !validID(p.VersionID) {
		return errors.New("入口名称或发布版本无效")
	}
	if len(p.AllowedOrigins) < 1 || len(p.AllowedOrigins) > 20 {
		return errors.New("请配置 1 到 20 个允许嵌入的网站")
	}
	origins := []string{}
	for _, raw := range p.AllowedOrigins {
		origin, err := normalizeWorkflowEmbedOrigin(raw)
		if err != nil {
			return err
		}
		if !slices.Contains(origins, origin) {
			origins = append(origins, origin)
		}
	}
	sort.Strings(origins)
	p.AllowedOrigins = origins
	if len(p.InputFields) > 32 || len(p.Mappings) < 1 || len(p.Mappings) > 1000 {
		return errors.New("输入字段或身份映射数量无效")
	}
	keys := map[string]bool{}
	for i, f := range p.InputFields {
		if !workflowInputKey.MatchString(f.Key) || keys[f.Key] || len([]rune(f.Label)) > 100 {
			return errors.New("输入字段 key 重复或格式无效")
		}
		keys[f.Key] = true
		if f.Label == "" {
			p.InputFields[i].Label = f.Key
		}
	}
	keys = map[string]bool{}
	for _, m := range p.Mappings {
		if !validWorkflowExternalSubject(m.Subject) || m.Username == "" || keys[m.Subject] {
			return errors.New("外部身份重复或格式无效")
		}
		keys[m.Subject] = true
	}
	return nil
}
func validWorkflowExternalSubject(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}
func validateWorkflowPublicationInputs(p workflowPublication, inputs map[string]string) (map[string]string, error) {
	if len(inputs) > 32 {
		return nil, errors.New("输入字段过多")
	}
	out := map[string]string{}
	known := map[string]bool{}
	total := 0
	for _, f := range p.InputFields {
		known[f.Key] = true
		v := inputs[f.Key]
		if f.Required && strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("请填写%s", f.Label)
		}
		if len(v) > 16<<10 {
			return nil, fmt.Errorf("%s内容过长", f.Label)
		}
		if _, ok := inputs[f.Key]; ok {
			out[f.Key] = v
			total += len(v)
		}
	}
	for key := range inputs {
		if !known[key] {
			return nil, errors.New("包含未声明的输入字段")
		}
	}
	if total > 64<<10 {
		return nil, errors.New("输入内容总长度过大")
	}
	return out, nil
}
