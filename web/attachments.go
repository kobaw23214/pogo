// Session attachments: uploaded files land in the session's own workspace,
// under uploads/, where the read tools can reach them (a path the session
// already guards) and the file tree can show them. What an attachment is:
// images the model may see natively, documents the docs skill parses, and
// audio/video the browser can preview — the store's job is naming, validation
// and the per-file metadata cache under uploads/.cache/ (hidden from listings).
package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yosukeno/pogo/prefs"

	_ "image/jpeg"
	_ "image/png"
)

const (
	// attachMaxBytes caps one upload. Images arrive already downscaled by the
	// composer; documents and media use 25 MB by default. The runtime override
	// lives in prefs (settings dialog slider); the ceiling's floor/limit stay
	// in effectiveAttachMaxMB.
	attachMaxBytes = 25 << 20
	// UploadsDir is the session-relative directory every attachment lands in.
	UploadsDir = "uploads"
	// attachCacheDir holds per-file metadata and description caches, hidden
	// from the file tree the way .git is.
	attachCacheDir = ".cache"
)

// attachTextExts are the document and text types the docs skill knows how to
// read, plus the plain text a read tool handles without any skill. An upload
// outside both lists is refused with the list, so a mis-aimed drag of a
// 2 GB binary fails at the door with an explanation rather than inside a run.
var attachDocExts = map[string]string{
	".pdf":  "pdf",
	".doc":  "doc", // legacy OLE Word: read with antiword, not the docx scripts
	".docx": "word",
	".xlsx": "excel",
	".csv":  "csv",
	".tsv":  "csv",
}
var attachMediaExts = map[string]string{
	".mp3": "audio", ".wav": "audio", ".ogg": "audio", ".m4a": "audio",
	".mp4": "video", ".webm": "video", ".mov": "video",
}

var attachTextExts = map[string]bool{
	".txt": true, ".md": true, ".json": true, ".xml": true, ".html": true,
	".htm": true, ".yaml": true, ".yml": true, ".log": true, ".ini": true,
	".conf": true, ".py": true, ".go": true, ".js": true, ".ts": true,
	".sh": true, ".sql": true,
}

// imageExts maps a sniffed image content type to its storage extension; SVG
// stays out (script-in-a-document, and no provider here accepts it). Gif and
// bmp are stored faithfully for raw uploads; the composer re-encodes them to
// jpeg before uploading, which is what the vision providers actually take.
var attachImageExts = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/webp": ".webp",
	"image/gif":  ".gif",
	"image/bmp":  ".bmp",
}

// attachGroups are the configurable classes the settings dialog offers. The
// allowlist is a subset chosen by group toggles or per-extension. Text, code
// and binaries are opt-in.
type AttachGroup struct {
	ID   string   `json:"id"`
	Exts []string `json:"exts"`
}

var attachGroups = []AttachGroup{
	{"image", []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp"}},
	{"office", []string{".pdf", ".doc", ".docx", ".xlsx", ".csv", ".tsv"}},
	{"media", []string{".mp3", ".wav", ".ogg", ".m4a", ".mp4", ".webm", ".mov"}},
	{"text", []string{
		".txt", ".md", ".markdown", ".rst", ".log",
		".json", ".json5", ".xml", ".yaml", ".yml", ".toml",
		".ini", ".conf", ".cfg", ".env", ".properties",
		".html", ".htm", ".css", ".scss", ".less", ".vue", ".svelte",
		".py", ".ipynb", ".go", ".rs", ".c", ".h", ".cpp", ".hpp", ".cc", ".cxx",
		".java", ".kt", ".kts", ".scala", ".swift", ".m", ".mm",
		".js", ".jsx", ".ts", ".tsx", ".php", ".rb", ".pl", ".pm",
		".lua", ".r", ".jl", ".dart",
		".sh", ".bash", ".zsh", ".fish", ".bat", ".cmd", ".ps1",
		".sql", ".proto", ".graphql", ".tex", ".bib", ".diff", ".patch",
	}},
	// Samples and archives: no parser reads these — they land in uploads/ and
	// the model reaches them with bash and the analysis tools, which for this
	// deployment is the point of uploading an exe at all. Legacy office
	// formats live here too (the docs skill speaks docx/xlsx, not doc/xls).
	{"binary", []string{
		".exe", ".dll", ".sys", ".msi", ".bin", ".dat",
		".zip", ".7z", ".rar", ".gz", ".tgz", ".bz2", ".xz", ".tar",
		".iso", ".img", ".apk", ".jar",
		".elf", ".so", ".dylib", ".o", ".a",
		".pcap", ".pcapng", ".dmp", ".evtx",
		".xls", ".ppt", ".pptx",
	}},
}

// attachBinaryExts is the binary group as a set — the branch saveAttachment
// classifies by (stored as-is, announced by path, no parsing).
var attachBinaryExts = func() map[string]bool {
	for _, g := range attachGroups {
		if g.ID == "binary" {
			out := make(map[string]bool, len(g.Exts))
			for _, e := range g.Exts {
				out[e] = true
			}
			return out
		}
	}
	return nil
}()

// defaultAttachExts is the allowlist with no override: image, office and media.
func defaultAttachExts() []string {
	var out []string
	for _, g := range attachGroups {
		if g.ID == "image" || g.ID == "office" || g.ID == "media" {
			out = append(out, g.Exts...)
		}
	}
	return out
}

// attachUniverse is every extension any group knows — the bounds a saved
// override may not exceed, because past it saveAttachment has no kind to
// classify the file into and would reject it anyway.
func attachUniverse() map[string]bool {
	out := make(map[string]bool)
	for _, g := range attachGroups {
		for _, e := range g.Exts {
			out[e] = true
		}
	}
	return out
}

// normalizeExts lowercases, dot-prefixes and dedupes an override list. An
// input that normalises to nothing is nil: absent and empty are the same
// choice (follow the default), and storing an empty array would make the
// settings dialog render "nothing allowed" as a saved state.
func normalizeExts(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, e := range in {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		if e == "." || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

// effectiveAttachExts resolves the override against the default. An override
// containing extensions outside the universe is dropped wholesale rather than
// partially honoured: half a hand-edited prefs.json is a mistake, and the
// safe reading of a mistake in a gate is the default, not a guess.
func effectiveAttachExts(override []string) map[string]bool {
	override = normalizeExts(override)
	if len(override) == 0 {
		override = defaultAttachExts()
	} else {
		u := attachUniverse()
		for _, e := range override {
			if !u[e] {
				override = defaultAttachExts()
				break
			}
		}
	}
	out := make(map[string]bool, len(override))
	for _, e := range override {
		out[e] = true
	}
	return out
}

// sortedExts renders an allowlist for the refusal message: sorted so the same
// set always reads the same way.
func sortedExts(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for e := range set {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// The upload cap's bounds. The floor keeps the door from refusing files the
// docs skill exists to read; the ceiling keeps per-upload in-memory buffering
// a bounded cost — every concurrent upload may hold up to this much while it
// is validated.
const (
	attachMinMB = 10
	attachMaxMB = 1024
)

// effectiveAttachMaxMB resolves the override into the cap actually enforced.
// 0 (and absent) mean the built-in default; a hand-edited value outside the
// bounds is clamped rather than honoured — same reading of a mistake as the
// allowlist's: the bounds, not the typo, decide.
func effectiveAttachMaxMB(override int) int {
	if override <= 0 {
		return attachMaxBytes >> 20
	}
	if override < attachMinMB {
		return attachMinMB
	}
	if override > attachMaxMB {
		return attachMaxMB
	}
	return override
}

// AttachmentMeta is what the composer learns about an upload and what the
// transcript's file blocks carry.
type AttachmentMeta struct {
	// Path is session-workspace relative ("uploads/report.docx") — the one
	// spelling the read tools, the file tree and the message note all share.
	Path string `json:"path"`
	Name string `json:"name"`
	// Kind is "image", "audio", "video", a docs-skill kind ("pdf"…), or "text".
	Kind string `json:"kind"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
	// W/H are set for images (display metadata, not a gate).
	W int `json:"w,omitempty"`
	H int `json:"h,omitempty"`
	// Created is when this upload landed.
	Created time.Time `json:"created"`
}

// The bounds a client-supplied folder path may take. Depth keeps a tree a
// tree (a symlink loop walked naively would be unbounded); the segment count
// over the total length is what a workspace path in a message stays readable
// at. Eight levels is deeper than any real upload wants and shallower than
// the pathological cases.
const attachMaxDepth = 8

// sanitizeRelDir reduces a client-supplied folder path (slash-separated,
// relative to uploads/) to sanitised segments joined by "/". Every segment
// gets the same character diet as a filename, and "." / ".." are refused
// outright rather than trimmed around: a path field from an upload form is a
// textbook traversal vector (the OpenOLAT and python-multipart CVEs were both
// exactly this shape), so the answer to ".." is no, not "probably fine".
func sanitizeRelDir(dir string) (string, error) {
	dir = strings.TrimSpace(strings.ReplaceAll(dir, "\\", "/"))
	if dir == "" || dir == "." {
		return "", nil
	}
	parts := strings.Split(dir, "/")
	if len(parts) > attachMaxDepth {
		return "", fmt.Errorf("目录层级过深（最多 %d 层）", attachMaxDepth)
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		if p == "." || p == ".." {
			return "", fmt.Errorf("非法目录路径 %q", dir)
		}
		s := sanitizeName(p)
		if s == "" {
			return "", fmt.Errorf("目录名无法使用 %q", p)
		}
		out = append(out, s)
	}
	return strings.Join(out, "/"), nil
}

// saveAttachment validates and stores one upload in the session's workspace.
// The name is sanitised to a single path element; subdir (already sanitised,
// "" for a flat upload) is the folder tree under uploads/ the file keeps —
// folder uploads preserve their structure rather than flattening, because a
// workspace that can ls a tree beats 200 colliding files named main.go. A
// collision gets a numeric suffix rather than an overwrite, because two
// uploads named "报告.docx" in one session are two different files the user
// meant to keep.
func saveAttachment(cwd, subdir, name, mime string, allowed map[string]bool, maxBytes int64, r io.Reader) (AttachmentMeta, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return AttachmentMeta{}, err
	}
	if int64(len(data)) > maxBytes {
		return AttachmentMeta{}, fmt.Errorf("附件超过 %d MB 上限", maxBytes>>20)
	}
	if len(data) == 0 {
		return AttachmentMeta{}, fmt.Errorf("空文件")
	}

	meta, base, ext, err := classifyAttachmentData(name, data, allowed, attachTextExts)
	if err != nil {
		return AttachmentMeta{}, err
	}
	dir := filepath.Join(cwd, UploadsDir)
	if subdir != "" {
		dir = filepath.Join(dir, filepath.FromSlash(subdir))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return AttachmentMeta{}, err
	}
	final := base + ext
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, final)); os.IsNotExist(err) {
			break
		}
		final = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
	if err := os.WriteFile(filepath.Join(dir, final), data, 0o644); err != nil {
		return AttachmentMeta{}, err
	}
	meta.Name = final
	meta.Path = path.Join(UploadsDir, subdir, final)

	// Metadata beside the bytes, keyed by content hash: re-uploads of the
	// same content anywhere in the session share one description cache.
	sum := sha256.Sum256(data)
	cacheMeta(sum, cwd, meta)
	return meta, nil
}

// classifyAttachmentData is pure classification/naming; storage is deliberately
// left to the caller so private sessions can use an os.Root descriptor.
func classifyAttachmentData(name string, data []byte, allowed, textExts map[string]bool) (AttachmentMeta, string, string, error) {
	meta := AttachmentMeta{Size: int64(len(data)), Created: time.Now()}
	ext := strings.ToLower(filepath.Ext(name))
	sniffed := http.DetectContentType(data)

	if e, ok := attachImageExts[sniffed]; ok && allowed[e] {
		meta.Kind = "image"
		meta.Mime = sniffed
		if ext != e {
			ext = e
		}
		if w, h, ok := imageDims(data, sniffed); ok {
			meta.W, meta.H = w, h
		}
	} else if kind, ok := attachDocExts[ext]; ok && allowed[ext] {
		meta.Kind = kind
		meta.Mime = sniffed
	} else if kind, ok := attachMediaExts[ext]; ok && allowed[ext] {
		meta.Kind = kind
		meta.Mime = sniffed
	} else if textExts[ext] && allowed[ext] {
		meta.Kind = "text"
		meta.Mime = sniffed
	} else if attachBinaryExts[ext] && allowed[ext] {
		// No parsing, no inline rendering: the bytes land in uploads/ and the
		// run's file block says where they are. What the model does next —
		// file, strings, capa — is exactly what this deployment uploads a
		// sample for.
		meta.Kind = "binary"
		meta.Mime = sniffed
	} else {
		return AttachmentMeta{}, "", "", fmt.Errorf(
			"不支持的附件类型 %q（当前允许：%s）", ext, strings.Join(sortedExts(allowed), " "))
	}

	base := sanitizeName(strings.TrimSuffix(path.Base(name), filepath.Ext(name)))
	if base == "" {
		base = "attachment"
	}
	return meta, base, ext, nil
}

// cacheMeta writes the upload's metadata under the hash key. Failures are
// cosmetic (the file works; only previews lose a dimension), so they are
// swallowed — same trade as the description cache.
func cacheMeta(sum [32]byte, cwd string, meta AttachmentMeta) {
	dir := filepath.Join(cwd, UploadsDir, attachCacheDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), raw, 0o644)
}

// attachAbs resolves a session-relative attachment path to an absolute one,
// refusing anything that tries to leave the workspace — the same coarse
// sandbox the file tools apply, arrived at from the upload side.
func attachAbs(cwd, rel string) (string, error) {
	clean := path.Clean("/" + rel)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("bad attachment path %q", rel)
	}
	abs := filepath.Join(cwd, filepath.FromSlash(clean))
	// Belt over braces: the join above is already inside cwd.
	if !withinRoot(cwd, abs) {
		return "", fmt.Errorf("attachment escapes the workspace: %q", rel)
	}
	return abs, nil
}

// attachmentURL is the bytes' content hash, used only to key description
// caches — attachments are addressed by path everywhere else.
func attachmentHash(cwd, rel string) (string, error) {
	abs, err := attachAbs(cwd, rel)
	if err != nil {
		return "", err
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// withinRoot reports whether p is root or inside it, on the same clean-join
// basis the tools' resolve uses.
func withinRoot(root, p string) bool {
	r := filepath.Clean(root)
	q := filepath.Clean(p)
	return q == r || strings.HasPrefix(q, r+string(os.PathSeparator))
}

// sanitizeName keeps one display-worthy path element: no separators, no dot
// prefix, no control characters, and a length a chip can show.
func sanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r > 0x2e00 && r < 0xa000, r == ' ', r == '-', r == '_', r == '.':
			return r // CJK and ordinary punctuation
		default:
			return -1
		}
	}, s)
	s = strings.TrimLeft(s, ".")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// imageDims reads pixel dimensions without a webp decoder (unknown stays
// unknown); guarded by the caller having sniffed a known image type.
func imageDims(data []byte, mime string) (int, int, bool) {
	switch mime {
	case "image/png", "image/jpeg":
		if cfg, _, err := image.DecodeConfig(strings.NewReader(string(data))); err == nil {
			return cfg.Width, cfg.Height, true
		}
	}
	return 0, 0, false
}

// isImageExt reports an image extension the composer may upload. Used at send
// time, where the bytes are on disk but re-sniffing them for a routing
// decision would read the whole file for a fact the extension already states.
func isImageExt(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp":
		return true
	}
	return false
}

// extToMime is the extension→mime mapping for that same decision path.
func extToMime(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	}
	return ""
}

// fileKindByExt classifies a non-image attachment for its transcript block;
// the empty kind renders as a plain file note.
func fileKindByExt(ext string) string {
	if kind, ok := attachDocExts[ext]; ok {
		return kind
	}
	if kind, ok := attachMediaExts[ext]; ok {
		return kind
	}
	if attachTextExts[ext] {
		return "text"
	}
	if attachBinaryExts[ext] {
		return "binary"
	}
	return ""
}

// --- HTTP ---

// handleSessionAttach accepts one file under multipart field "file" and lands
// it in the session's workspace uploads/. The session is the scope: a file
// uploaded into one conversation belongs to that conversation's working
// directory, where its read tools and file tree already operate.
func (s *Server) handleSessionAttach(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	if s.rejectBackgroundTaskWrite(w, sess.ID) {
		return
	}
	// Both knobs live in prefs (settings dialog) and are read per upload, so
	// a change applies without a restart. A file that cannot be read fails
	// open to the defaults — restrictive, and the upload still gets a
	// decision rather than an error about configuration. Read before the
	// multipart parsing: its memory boundary and the body cap below are what
	// make the limit real at the transport, not just at the door.
	p := prefs.Default()
	if loaded, err := prefs.Load(s.prefsPath()); err == nil {
		p = loaded
	}
	maxBytes := int64(effectiveAttachMaxMB(p.AttachmentMaxMB)) << 20

	if err := r.ParseMultipartForm(maxBytes); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	fhs := r.MultipartForm.File["file"]
	if len(fhs) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("no file field"))
		return
	}
	f, err := fhs[0].Open()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	defer f.Close()
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	// "dir" is the folder-tree half of a folder upload (uploads/<dir>/<file>);
	// absent for a plain one. It is client-supplied path material, so it goes
	// through sanitizeRelDir before anything is joined to it — the per-request
	// gate the traversal CVEs in other upload stacks were missing.
	subdir, err := sanitizeRelDir(r.FormValue("dir"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	meta, err := saveAttachment(sess.cwd, subdir, fhs[0].Filename, fhs[0].Header.Get("Content-Type"),
		effectiveAttachExts(p.AttachmentExts), maxBytes, f)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// The ledger is how Delete attributes this file to the session even if it
	// never reaches a message; sent attachments the transcript remembers too.
	noteSessionUpload(sess.cwd, sess.ID, meta.Path)
	if err := sess.registerManagedOfficeAttachment(meta.Path); err != nil {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("附件已保存，但文档修改登记尚未完成，请重新读取附件列表后重试：%w", err))
		return
	}
	// Published so every attached pane learns the upload exists the moment it
	// lands — the file tree refreshes, the session-files list counts it —
	// rather than when a message happens to carry the path.
	sess.hub.Publish(Event{Type: EvAttachmentUploaded, Attachment: &meta})
	sess.touch()
	writeJSON(w, http.StatusOK, meta)
}

// handleSessionAttachments lists what the session has uploaded into its
// workspace: the session-files popover's initial state, which live
// attachment_uploaded events then keep current. The union of the upload
// ledger and the transcript's own blocks — the same two witnesses Delete
// attributes by — so sessions written before the ledger existed still list;
// files that no longer exist on disk drop out.
func (s *Server) handleSessionAttachments(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	refs, _ := sessionUploadRefs(sess.Path())
	paths := append(refs, sessionUploadLedger(sess.cwd, sess.ID)...)
	seen := map[string]bool{}
	list := []AttachmentMeta{}
	for _, rel := range paths {
		if seen[rel] || !isUploadRef(rel) {
			continue
		}
		seen[rel] = true
		meta, ok := attachmentMetaFor(sess.cwd, rel)
		if !ok {
			continue
		}
		list = append(list, meta)
	}
	writeJSON(w, http.StatusOK, map[string]any{"attachments": list})
}

// attachmentMetaFor rebuilds one upload's listing metadata from the
// filesystem: size and mtime from stat, kind from the same extension tables
// saveAttachment classifies with. Image pixel dimensions stay uncached reads
// the listing does not need — the composer thumbnails by path regardless.
func attachmentMetaFor(cwd, rel string) (AttachmentMeta, bool) {
	abs, err := attachAbs(cwd, rel)
	if err != nil {
		return AttachmentMeta{}, false
	}
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		return AttachmentMeta{}, false
	}
	name := path.Base(rel)
	ext := strings.ToLower(filepath.Ext(name))
	meta := AttachmentMeta{
		Path: rel, Name: name, Size: st.Size(), Created: st.ModTime(),
		Kind: fileKindByExt(ext),
	}
	if isImageExt(ext) {
		meta.Kind = "image"
		meta.Mime = extToMime(ext)
	}
	return meta, true
}
