package web

// Shortcut attachments are immutable snapshots, outside conversation trees.
// A Studio save deliberately publishes the snapshot to that Agent; reads still
// require an owned conversation bound to the same Agent. Ordinary shortcuts
// publish to the server's global starter collection, matching /api/starters.
// The wire never carries host paths or reads the creator's workspace on reuse.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/prefs"
	"github.com/yosukeno/pogo/skills"
)

type starterAssetInput struct {
	Prompt      string             `json:"prompt,omitempty"`
	Parts       []MessageInputPart `json:"parts"`
	InputLayout string             `json:"input_layout,omitempty"`
}

type starterAssetResult struct {
	starterAssetInput
	Attachments []AttachmentMeta `json:"attachments"`
}

type starterAssetRecord struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func (s *Server) starterAssetNamespace(_ *http.Request, sess *Session) string {
	return s.starterAssetNamespaceForAgent(sess.studioID)
}

func (s *Server) starterAssetNamespaceForAgent(agentID string) string {
	key := "global-starters"
	if agentID != "" {
		key = "agent:" + agentID
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.mgr.cfg.SessionDir, ".starter-assets", hex.EncodeToString(sum[:]))
}

func openStarterAssetRoot(dir string) (*os.Root, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenRoot(dir)
}

func readStarterAsset(root *os.Root, handle string) (*os.File, starterAssetRecord, error) {
	var meta starterAssetRecord
	if msg := skills.ValidateStarterParts([]MessageInputPart{{Type: "file", Path: handle}}, ""); msg != "" {
		return nil, meta, errors.New("无效的快捷附件")
	}
	segments := strings.Split(handle, "/")
	data, err := root.ReadFile(segments[1] + "/meta.json")
	if err != nil || json.Unmarshal(data, &meta) != nil || meta.Name != segments[2] || meta.Size <= 0 || meta.Size > int64(attachMaxMB)<<20 || len(meta.SHA256) != 64 {
		return nil, meta, errors.New("快捷附件不可用，请重新添加文件并保存卡片")
	}
	f, err := root.Open(segments[1] + "/data")
	if err != nil {
		return nil, meta, errors.New("快捷附件不可用，请重新添加文件并保存卡片")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != meta.Size {
		f.Close()
		return nil, meta, errors.New("快捷附件已损坏，请重新保存卡片")
	}
	return f, meta, nil
}

func copyStarterFile(dst io.Writer, src io.Reader, limit int64) (int64, string, error) {
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(src, limit+1))
	if err != nil {
		return n, "", err
	}
	if n > limit {
		return n, "", fmt.Errorf("快捷附件超过 %d MB 上限", limit>>20)
	}
	return n, hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Server) handleStarterAssetsSave(w http.ResponseWriter, r *http.Request) {
	s.starterAssets(w, r, false)
}

func (s *Server) handleStarterAssetsResolve(w http.ResponseWriter, r *http.Request) {
	s.starterAssets(w, r, true)
}

func (s *Server) starterAssets(w http.ResponseWriter, r *http.Request, resolve bool) {
	s.savedInputAssets(w, r, resolve, false)
}

func (s *Server) savedInputAssets(w http.ResponseWriter, r *http.Request, resolve, private bool) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	if s.rejectBackgroundTaskWrite(w, sess.ID) {
		return
	}
	if private && sess.studioID != "" {
		a, visible := s.mgr.studio.get(sess.studioID)
		if !visible || a.Status != "ready" && !s.adminRequest(r) {
			writeErr(w, 404, errors.New("Agent 不存在或不可访问"))
			return
		}
	}
	var input starterAssetInput
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := decodeBody(r, &input); err != nil {
		writeErr(w, 400, err)
		return
	}
	var validationErr error
	if private {
		if msg := skills.ValidateInputParts(input.Parts, input.InputLayout, "workspace"); msg != "" {
			validationErr = errors.New(msg)
		}
	} else {
		_, _, validationErr = normalizeMessageInput(input.Prompt, nil, input.Parts, input.InputLayout)
	}
	if err := validationErr; err != nil || input.Parts == nil {
		if err == nil {
			err = errors.New("请提供快捷卡片内容")
		}
		writeErr(w, 400, err)
		return
	}
	if resolve {
		mode := "starter"
		if private {
			mode = "shortcut"
		}
		if msg := skills.ValidateInputParts(input.Parts, input.InputLayout, mode); msg != "" {
			writeErr(w, 400, errors.New(msg))
			return
		}
	}
	var err error
	assetNamespace := s.starterAssetNamespace(r, sess)
	if private {
		assetNamespace, err = s.mgr.shortcutAssetNamespace(sess.owner, sess.studioID)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
	}
	assetRoot, err := openStarterAssetRoot(assetNamespace)
	if err != nil {
		writeErr(w, 500, errors.New("无法打开快捷附件存储"))
		return
	}
	defer assetRoot.Close()
	privateNamespace, e := s.mgr.shortcutAssetNamespace(sess.owner, sess.studioID)
	if e != nil {
		writeErr(w, 400, e)
		return
	}
	privateRoot := assetRoot
	if !private {
		privateRoot, e = openStarterAssetRoot(privateNamespace)
		if e != nil {
			writeErr(w, 500, errors.New("无法打开资料附件存储"))
			return
		}
		defer privateRoot.Close()
	}
	workspace, err := os.OpenRoot(sess.cwd)
	if err != nil {
		writeErr(w, 500, errors.New("无法打开会话工作区"))
		return
	}
	defer workspace.Close()
	p := prefs.Default()
	if loaded, e := prefs.Load(s.prefsPath()); e == nil {
		p = loaded
	}
	maxBytes := int64(effectiveAttachMaxMB(p.AttachmentMaxMB)) << 20
	result := starterAssetResult{starterAssetInput: input, Attachments: []AttachmentMeta{}}
	result.Parts = inputref.Clone(input.Parts)
	mapped := map[string]string{}
	type ownedAsset struct {
		root *os.Root
		dir  string
	}
	var ownedAssets []ownedAsset
	var ownedUploads []string
	committed := false
	defer func() {
		if committed {
			return
		}
		for _, item := range ownedAssets {
			_ = item.root.RemoveAll(item.dir)
		}
		for _, dir := range ownedUploads {
			_ = workspace.RemoveAll(dir)
		}
	}()
	privateFiles := inputPrivateFiles(result.Parts)
	for _, targetPart := range inputPartPointers(result.Parts) {
		part := *targetPart
		if part.Type != "file" {
			continue
		}
		usePrivate := private || privateFiles[targetPart] || strings.HasPrefix(part.Path, "shortcut-assets/")
		currentRoot := assetRoot
		if usePrivate {
			currentRoot = privateRoot
		}
		if private && strings.HasPrefix(part.Path, "starter-assets/") {
			writeErr(w, 400, errors.New("资料附件必须属于当前账号及 Agent"))
			return
		}
		mapKey := part.Path
		if usePrivate {
			mapKey = "private:" + mapKey
		}
		if reused, found := mapped[mapKey]; found {
			targetPart.Path = reused
			continue
		}
		if !resolve && (strings.HasPrefix(part.Path, "starter-assets/") || strings.HasPrefix(part.Path, "shortcut-assets/")) {
			f, _, e := readStarterAsset(currentRoot, part.Path)
			if e != nil {
				writeErr(w, 400, e)
				return
			}
			f.Close()
			mapped[mapKey] = part.Path
			continue
		}
		var source *os.File
		var record starterAssetRecord
		if resolve {
			source, record, err = readStarterAsset(currentRoot, part.Path)
		} else {
			source, err = workspace.Open(part.Path)
			if err == nil {
				var info os.FileInfo
				info, err = source.Stat()
				if err == nil && (!info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxBytes) {
					err = errors.New("文件为空、不是普通文件或超过当前附件大小上限")
				}
				if err == nil {
					name := strings.ToValidUTF8(sanitizeName(path.Base(part.Path)), "")
					if name == "" {
						name = "附件"
					}
					record = starterAssetRecord{Name: name, Size: info.Size()}
				}
			}
		}
		if err != nil {
			if source != nil {
				source.Close()
			}
			writeErr(w, 400, errors.New("无法读取快捷附件："+path.Base(part.Path)))
			return
		}
		id := randomID(16)
		var target *os.File
		var destination string
		if resolve {
			folder := "starters"
			if usePrivate {
				folder = "shortcuts"
			}
			dir := path.Join(UploadsDir, folder, id)
			err = workspace.MkdirAll(dir, 0o755)
			if err == nil {
				ownedUploads = append(ownedUploads, dir)
				destination = path.Join(dir, record.Name)
				target, err = workspace.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			}
		} else {
			err = currentRoot.Mkdir(id, 0o700)
			if err == nil {
				ownedAssets = append(ownedAssets, ownedAsset{currentRoot, id})
				target, err = currentRoot.OpenFile(id+"/data", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			}
		}
		if err != nil {
			source.Close()
			writeErr(w, 500, errors.New("无法保存快捷附件"))
			return
		}
		limit := maxBytes
		if resolve {
			limit = record.Size
		}
		written, hash, copyErr := copyStarterFile(target, source, limit)
		source.Close()
		closeErr := target.Close()
		if copyErr != nil || closeErr != nil || written != record.Size || (resolve && hash != record.SHA256) {
			writeErr(w, 400, errors.New("快捷附件复制失败或内容已变化，请重新保存"))
			return
		}
		if !resolve {
			record.SHA256 = hash
			metadata, _ := json.Marshal(record)
			if err = currentRoot.WriteFile(id+"/meta.json", metadata, 0o600); err != nil {
				writeErr(w, 500, errors.New("无法登记快捷附件"))
				return
			}
			prefix := "starter-assets"
			if usePrivate {
				prefix = "shortcut-assets"
			}
			destination = path.Join(prefix, id, record.Name)
		} else {
			meta, exists := attachmentMetaFor(sess.cwd, destination)
			if !exists {
				writeErr(w, 500, errors.New("无法登记会话附件"))
				return
			}
			result.Attachments = append(result.Attachments, meta)
		}
		mapped[mapKey] = destination
		targetPart.Path = destination
	}
	// Resolved files are ordinary session attachments from here on; they never
	// depend on the creator's workspace or on the next card edit.
	committed = true
	if resolve {
		for _, meta := range result.Attachments {
			noteSessionUpload(sess.cwd, sess.ID, meta.Path)
			if err := sess.registerManagedOfficeAttachment(meta.Path); err != nil {
				writeErr(w, 503, errors.New("快捷附件已复制，但文档登记尚未完成，请重新读取附件列表后重试"))
				return
			}
		}
		for _, meta := range result.Attachments {
			sess.hub.Publish(Event{Type: EvAttachmentUploaded, Attachment: &meta})
		}
	}
	result.Prompt = starterInputFallback(result.Parts)
	writeJSON(w, http.StatusOK, result)
}
