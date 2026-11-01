package web

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skillruntime/core"
	"github.com/yosukeno/pogo/skills"
)

// Skill upload: the skills sheet installs a zip of a skill bundle at runtime.
//
// Trust: this endpoint is behind the same token as everything else under /api,
// and installing a skill is the same power the token already grants — the bash
// tool can write the skill directory directly. What the endpoint adds is not
// privilege but convenience: structure validation, a safe extraction, and a
// reload that makes the bundle live without a restart.
//
// The panel's SKILL.md viewer stays read-only (see SkillsPanel); writing whole
// bundles from a zip is a different act from editing prompt text in place, and
// keeping the two separate keeps the viewer's "no writable path to prompt
// injection" property intact.

// Upload limits. Size itself is not capped by default: bundles carry real
// references and data, and the deployment decides its own ceiling through
// POGO_SKILL_UPLOAD_MAX_BYTES (compressed bytes; 0 or unset means unlimited).
// What stays bounded are the shapes no honest bundle needs — entry count,
// nesting depth, and a decompression ratio — so a zip bomb still fails
// somewhere cheap rather than at disk-full.
const (
	skillZipMaxFiles  = 2000
	skillZipMaxTopics = 8 // directory nesting depth
	// skillBombRatio bounds decompressed bytes per compressed byte. Real
	// reference material compresses far below this; only adversarial padding
	// crosses it.
	skillBombRatio = 10
)

// skillUploadMaxBytes resolves the compressed-size ceiling for one upload.
// Zero — the default — is an explicit "no limit"; a negative or unparseable
// value is read as the deployment saying nothing, which is also no limit.
func skillUploadMaxBytes() int64 {
	raw := strings.TrimSpace(os.Getenv("POGO_SKILL_UPLOAD_MAX_BYTES"))
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// skillNameRE is the directory name an installed bundle may take. Stricter
// than the loader's warning-only name check because this is a path segment we
// create, not a label we display.
var skillNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// handleSkillUpload installs a skill zip. Two wire shapes are accepted: a
// multipart form with the archive under field "file" (the browser), or the
// archive as the whole request body with Content-Type application/zip
// (curl --data-binary), because a UI upload and a scripted install should not
// need two endpoints. Either way the bytes stream straight to a staging file
// beside the skills directory — never through memory, and never through /tmp,
// which in a container is routinely a small tmpfs.
func (s *Server) handleSkillUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	opts := s.mgr.SkillOptions()
	dir := opts.UserDir
	if dir == "" {
		dir = skills.UserDir()
	}
	if dir == "" {
		writeErr(w, http.StatusInternalServerError, errors.New("no user skills directory available"))
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// The staging root is a sibling of the skills directory: same filesystem,
	// so the final install is a rename, and outside the loader's scan, so a
	// half-written zip is never candidate skill material.
	stageRoot := filepath.Join(filepath.Dir(dir), ".skill-uploads")
	if s.mgr.cfg.SkillRuntime != nil {
		// Tool runtimes/data must stay outside every Agent workspace, including
		// while the unified endpoint is still receiving their ZIP bytes.
		stageRoot = filepath.Join(s.mgr.cfg.SkillRuntime.Root, ".skill-uploads")
	}
	if err := os.MkdirAll(stageRoot, 0o700); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	sweepStaleUploads(stageRoot)

	limit := skillUploadMaxBytes()
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/zip") {
		staged, size, err := streamToStage(r.Body, stageRoot, limit)
		if err != nil {
			s.replyUploadErr(w, err)
			return
		}
		s.installSkillZip(w, staged, size, "upload.zip", dir, stageRoot)
		return
	}

	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("reading upload: %w", err))
		return
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, errors.New(`upload field "file" is required`))
			return
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("reading upload: %w", err))
			return
		}
		if part.FormName() != "file" {
			io.Copy(io.Discard, part) // a foreign part is not a reason to fail
			continue
		}
		zipName := part.FileName()
		if zipName == "" || !strings.HasSuffix(strings.ToLower(zipName), ".zip") {
			part.Close()
			writeErr(w, http.StatusBadRequest, errors.New("only .zip uploads are accepted"))
			return
		}
		staged, size, err := streamToStage(part, stageRoot, limit)
		part.Close()
		if err != nil {
			s.replyUploadErr(w, err)
			return
		}
		s.installSkillZip(w, staged, size, zipName, dir, stageRoot)
		return
	}
}

// streamToStage copies one upload stream into a fresh staging file, counting
// bytes so a configured ceiling can cut the transfer off mid-flight instead of
// after it landed. The caller owns the file (and its cleanup) from here on.
func streamToStage(src io.Reader, stageRoot string, limit int64) (string, int64, error) {
	f, err := os.CreateTemp(stageRoot, "zip-*.tmp")
	if err != nil {
		return "", 0, err
	}
	name := f.Name()
	var n int64
	if limit > 0 {
		n, err = io.Copy(f, io.LimitReader(src, limit+1))
	} else {
		n, err = io.Copy(f, src)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		return "", 0, err
	}
	if limit > 0 && n > limit {
		os.Remove(name)
		return "", 0, fmt.Errorf("zip exceeds %d MB", limit>>20)
	}
	return name, n, nil
}

// sweepStaleUploads removes staging files abandoned by crashed or cancelled
// uploads. Anything older than a day is debris; anything younger might still
// be in flight on another request.
func sweepStaleUploads(stageRoot string) {
	entries, err := os.ReadDir(stageRoot)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.IsDir() {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(stageRoot, e.Name()))
		}
	}
}

// replyUploadErr keeps the wire contract of the old size check: a bad bundle
// (or a too-large one) is a 400 with the reason; a disk that ran out
// mid-copy is a 500 with the story told.
func (s *Server) replyUploadErr(w http.ResponseWriter, err error) {
	if isDiskFull(err) {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("staging the upload ran out of disk: %w", err))
		return
	}
	writeErr(w, http.StatusBadRequest, err)
}

func isDiskFull(err error) bool {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && pathErr.Err != nil {
		return strings.Contains(strings.ToLower(pathErr.Err.Error()), "no space")
	}
	return strings.Contains(strings.ToLower(err.Error()), "no space")
}

// stagedUpload owns one staged zip's lifecycle. A staged file is removed when
// the request ends — unless the upload parked it for a replace decision, in
// which case the confirm file outlives the request and the 24h sweep is its
// only cleanup.
type stagedUpload struct {
	path string
	keep bool
}

func (u *stagedUpload) release() {
	if !u.keep {
		os.Remove(u.path)
	}
}

// installSkillZip validates and installs one staged zip file, then answers the
// request. Both wire shapes converge here.
func (s *Server) installSkillZip(w http.ResponseWriter, staged string, size int64, zipName, skillsDir, stageRoot string) {
	u := &stagedUpload{path: staged}
	defer u.release()
	packaged, err := runtimeSkillArchive(staged)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if packaged {
		s.installRuntimeSkill(w, u)
		return
	}
	// Ordinary Skills extract beside their final directory, so installing from
	// a private upload spool on another volume still finishes with a rename.
	stageRoot = filepath.Join(filepath.Dir(skillsDir), ".skill-uploads")
	if err := os.MkdirAll(stageRoot, 0700); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// Extraction staging beside the zip: same filesystem as the install
	// target, so the move into place is a rename rather than a copy.
	extract, err := os.MkdirTemp(stageRoot, "extract-*")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer os.RemoveAll(extract)

	zf, err := os.Open(staged)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer zf.Close()

	root, err := extractSkillZip(zf, size, extract)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	// The loader is the authority on what a valid skill is — the same check the
	// boot scan applies, so anything installable here is exactly what a restart
	// would also load. Loading by explicit path keeps the staging location out
	// of the ordinary scan order. Default locations must be disabled here: an
	// existing user Skill must neither count as another archive entry nor shadow
	// an invalid incoming document with the same name.
	loaded, _ := skills.Load(skills.Options{Paths: []string{root}, Disabled: true})
	if len(loaded) != 1 {
		writeErr(w, http.StatusBadRequest, errors.New(
			"no loadable skill found: the bundle needs a SKILL.md with a description (and only one skill per zip)"))
		return
	}
	name := loaded[0].Name
	if !skillNameRE.MatchString(name) || strings.Contains(name, "..") {
		writeErr(w, http.StatusBadRequest, fmt.Errorf(
			"skill name %q is not usable as an install directory (lowercase letters, digits, . _ -)", name))
		return
	}

	s.mgr.studio.runtimeSkillEdits.Lock()
	defer s.mgr.studio.runtimeSkillEdits.Unlock()
	target := filepath.Join(skillsDir, name)
	// An upload of the same name replaces what is there. The alternative —
	// refusing — makes iteration impossible without a delete affordance, and
	// the file on disk is recoverable the ordinary way (it is just files).
	if err := os.RemoveAll(target); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := moveDir(root, target); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// Reload with the boot options so flag-provided paths and the embedded set
	// stay in the picture, then swap the Manager's projections atomically.
	opts := s.mgr.SkillOptions()
	list, diags := skills.Load(opts)
	s.mgr.SetSkills(list)
	warnings := make([]string, 0, len(diags))
	for _, d := range diags {
		warnings = append(warnings, d.Path+": "+d.Message)
	}
	if s.opts.Logger != nil {
		s.opts.Logger.Printf("skill %q installed from upload (%s, %d bytes)", name, zipName, size)
	}
	ids := []string{}
	for _, skill := range list {
		if skill.Name == name {
			ids = append(ids, skillIDOf(skill))
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"skill_ids": ids,
		"name":      name,
		"path":      filepath.Join(target, "SKILL.md"),
		"warnings":  warnings,
	})
}

func runtimeSkillArchive(archive string) (bool, error) {
	return core.InspectSkillZIP(archive)
}

// confirmTokenRE names the parked-upload files: confirm-<token>.zip and the
// resolve-<token>.zip claim. 128 random bits, hex — unguessable and safe as
// one path segment.
var confirmTokenRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// skillReplaceConfirm collects other installed versions of the same package.
// An exact id+version match is not a decision — byte-identical dedup and
// content conflicts are answered by the install itself — so a re-upload of a
// version that is already present never asks.
func skillReplaceConfirm(service *skillruntime.Service, peek core.SkillZIPPeek) []map[string]any {
	var others []map[string]any
	for _, entry := range service.List() {
		if entry.Ref.ID != peek.ID {
			continue
		}
		if entry.Ref.Version == peek.Version {
			return nil
		}
		others = append(others, map[string]any{
			"skill_id":     skillruntime.PackageSkillID(entry.Ref),
			"version":      entry.Ref.Version,
			"installed_at": entry.InstalledAt,
			"revoked":      entry.Revoked,
		})
	}
	return others
}

// parkStagedUpload renames the staged zip to confirm-<token>.zip so it
// survives the request; the 24h staging sweep is the abandoned-decision
// cleanup.
func parkStagedUpload(staged string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	if err := os.Rename(staged, filepath.Join(filepath.Dir(staged), "confirm-"+token+".zip")); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Server) installRuntimeSkill(w http.ResponseWriter, u *stagedUpload) {
	service := s.mgr.cfg.SkillRuntime
	if service == nil {
		writeErr(w, http.StatusConflict, errors.New("Skill 工具运行环境尚未启用，请先配置服务的运行目录"))
		return
	}
	if peek, err := core.PeekSkillZIP(u.path); err == nil {
		if existing := skillReplaceConfirm(service, peek); existing != nil {
			token, err := parkStagedUpload(u.path)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			u.keep = true
			if s.opts.Logger != nil {
				s.opts.Logger.Printf("skill upload %s@%s parked for replace decision (%d other version(s))", peek.ID, peek.Version, len(existing))
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status":        "confirm_replace",
				"confirm_token": token,
				"incoming":      map[string]string{"id": peek.ID, "version": peek.Version, "name": peek.DisplayName},
				"existing":      existing,
			})
			return
		}
	}
	s.finishRuntimeSkillInstall(w, service, u.path, nil, nil)
}

// finishRuntimeSkillInstall installs the staged archive and answers with the
// install result; replaced lists the versions a replace decision removed and
// deferred the removals it could not complete.
func (s *Server) finishRuntimeSkillInstall(w http.ResponseWriter, service *skillruntime.Service, archive string, replaced, deferred []string) {
	installed, err := service.ImportFile(archive)
	if err != nil {
		status := http.StatusBadRequest
		var pathErr *os.PathError
		var linkErr *os.LinkError
		if errors.Is(err, core.ErrVersionConflict) {
			status = http.StatusConflict
		} else if errors.As(err, &pathErr) || errors.As(err, &linkErr) {
			status = http.StatusInternalServerError
		}
		writeErr(w, status, err)
		return
	}
	// The catalog gained a version: the MCP serve layer re-registers its
	// runtime tools off this hook.
	s.mgr.fireRuntimeSkillChange()
	warnings := append([]string{}, deferred...)
	if installed.Revoked {
		warnings = append(warnings, "此 Skill 版本已停用，请上传新的版本。")
	}
	for _, version := range replaced {
		warnings = append(warnings, "已按替换原则删除旧版本 v"+version+"。仍引用它的会话会保留，但不再能使用该 Skill。")
	}
	if replaced == nil {
		replaced = []string{}
	}
	// Installation stores a verified bundle; it does not establish that this
	// server can execute its binaries. Reuse the selection checks without
	// starting package code, and keep a completed install successful even when
	// its platform or execution environment still needs attention.
	skillID := skillruntime.PackageSkillID(installed.Ref)
	plan, planErr := service.PlanSelection(skillruntime.Selection{SkillIDs: []string{skillID}})
	issues := plan.Issues
	if issues == nil {
		issues = []skillruntime.SelectionIssue{}
	}
	if planErr != nil {
		issues = append(issues, skillruntime.SelectionIssue{Code: "SELECTION_INVALID", Message: planErr.Error()})
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"status": "installed", "ready": planErr == nil && plan.Ready,
		"execution": service.ExecutionStatus(), "issues": issues,
		"name": installed.Name, "skill_ids": []string{skillID},
		"version": installed.Ref.Version, "warnings": warnings, "replaced_versions": replaced,
	})
}

// handleSkillUploadResolve finishes a parked replace decision. The staged
// bytes never left the server; resolve only says what to do with them.
func (s *Server) handleSkillUploadResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Token  string `json:"token"`
		Action string `json:"action"` // "replace" | "keep-old"
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	service := s.mgr.cfg.SkillRuntime
	if service == nil {
		writeErr(w, http.StatusConflict, errors.New("Skill 工具运行环境尚未启用，请先配置服务的运行目录"))
		return
	}
	if !confirmTokenRE.MatchString(body.Token) {
		writeErr(w, http.StatusBadRequest, errors.New("确认凭据无效"))
		return
	}
	stageRoot := filepath.Join(service.Root, ".skill-uploads")
	parked := filepath.Join(stageRoot, "confirm-"+body.Token+".zip")
	claim := filepath.Join(stageRoot, "resolve-"+body.Token+".zip")
	// The rename is the claim: exactly one request can resolve a token.
	if err := os.Rename(parked, claim); err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, errors.New("此上传的确认已过期或已被处理，请重新上传"))
		} else {
			writeErr(w, http.StatusInternalServerError, err)
		}
		return
	}
	defer os.Remove(claim)
	switch body.Action {
	case "keep-old":
		if s.opts.Logger != nil {
			s.opts.Logger.Printf("parked skill upload %s discarded by keep-old decision", body.Token)
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "kept_old"})
		return
	case "replace":
	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("unknown action %q (replace or keep-old)", body.Action))
		return
	}
	peek, err := core.PeekSkillZIP(claim)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// The replace principle: install first, then the other versions of the
	// same package go — without the reference checks, so sessions keep their
	// transcripts and simply lose the Skill (dynamic unload). Definition
	// writes serialize against the removals exactly like an explicit delete;
	// a version with a call literally in flight stays (ErrPackageBusy) and is
	// reported rather than ripped out from under the process.
	s.mgr.studio.runtimeSkillEdits.Lock()
	installed, err := service.ImportFile(claim)
	var replaced, deferred []string
	if err == nil {
		for _, entry := range service.List() {
			if entry.Ref.ID != installed.Ref.ID || entry.Ref == installed.Ref {
				continue
			}
			if _, delErr := service.Delete(entry.Ref.PackageDigest); delErr != nil {
				deferred = append(deferred, fmt.Sprintf("旧版本 v%s 未能删除：%v", entry.Ref.Version, delErr))
				continue
			}
			replaced = append(replaced, entry.Ref.Version)
		}
	}
	s.mgr.studio.runtimeSkillEdits.Unlock()
	if err != nil {
		status := http.StatusBadRequest
		var pathErr *os.PathError
		var linkErr *os.LinkError
		if errors.Is(err, core.ErrVersionConflict) {
			status = http.StatusConflict
		} else if errors.As(err, &pathErr) || errors.As(err, &linkErr) {
			status = http.StatusInternalServerError
		}
		writeErr(w, status, err)
		return
	}
	if s.opts.Logger != nil {
		s.opts.Logger.Printf("skill %s@%s installed by replace decision; removed %d other version(s), %d deferred", peek.ID, peek.Version, len(replaced), len(deferred))
	}
	s.finishRuntimeSkillInstall(w, service, claim, replaced, deferred)
}

// extractSkillZip unpacks the archive under staging and returns the skill
// root: the staging dir itself when SKILL.md sits at the top, or the single
// top-level directory wrapping it — the two shapes every packaging tool
// produces. Everything else is a 400 with an explanation rather than a guess.
//
// The archive is read from the file it landed in (ReaderAt), entry by entry;
// memory stays O(1) however large the bundle. The decompressed total is
// bounded relative to what was uploaded — a fixed ceiling would either be too
// small for real data bundles or meaningless once uploads are unlimited, and
// a ratio catches the one shape that matters: an adversarial archive.
func extractSkillZip(src io.ReaderAt, size int64, staging string) (string, error) {
	zr, err := zip.NewReader(src, size)
	if err != nil {
		return "", fmt.Errorf("not a readable zip: %w", err)
	}

	budget := size * skillBombRatio
	var raw int64
	tops := map[string]bool{}
	for i, zf := range zr.File {
		if i >= skillZipMaxFiles {
			return "", fmt.Errorf("zip has more than %d entries", skillZipMaxFiles)
		}
		name := path.Clean(zf.Name)
		if name == "." || name == ".." || path.IsAbs(name) || strings.HasPrefix(name, "../") {
			// The zip-slip guard: an entry naming a path outside the archive's
			// own tree is damage or malice, and either way it stops here.
			return "", fmt.Errorf("unsafe entry name %q", zf.Name)
		}
		if depth := strings.Count(name, "/"); depth > skillZipMaxTopics {
			return "", fmt.Errorf("entry %q nests deeper than %d levels", zf.Name, skillZipMaxTopics)
		}
		top := name
		if i := strings.Index(top, "/"); i >= 0 {
			top = top[:i]
		}
		if top != "" {
			tops[top] = true
		}

		dest := filepath.Join(staging, filepath.FromSlash(name))
		// FromSlash keeps separators; Join cleans. The check after the join is
		// the one that matters on Windows, where a name like "C:evil" survives
		// the slash checks but escapes as an absolute-ish path.
		if !strings.HasPrefix(dest, staging+string(filepath.Separator)) && dest != staging {
			return "", fmt.Errorf("unsafe entry name %q", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		entry, err := zf.Open()
		if err != nil {
			return "", err
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, modeOf(zf))
		if err != nil {
			entry.Close()
			return "", err
		}
		n, err := io.Copy(out, io.LimitReader(entry, budget-raw+1))
		entry.Close()
		cerr := out.Close()
		if err == nil {
			err = cerr
		}
		if err != nil {
			return "", err
		}
		if raw += n; raw > budget {
			return "", fmt.Errorf("zip decompresses past %dx its compressed size", skillBombRatio)
		}
	}

	// Root detection: SKILL.md at the top level, or exactly one wrapper dir.
	if _, err := os.Stat(filepath.Join(staging, "SKILL.md")); err == nil {
		return staging, nil
	}
	if len(tops) == 1 {
		for top := range tops {
			root := filepath.Join(staging, top)
			if _, err := os.Stat(filepath.Join(root, "SKILL.md")); err == nil {
				return root, nil
			}
		}
	}
	return "", errors.New(
		`no SKILL.md found: the zip must contain SKILL.md at its root, or a single directory that contains it`)
}

// modeOf keeps the executable bit for scripts. Windows-built zips often lose
// it; scripts are then run through their interpreter, which every skill script
// header already names.
func modeOf(zf *zip.File) os.FileMode {
	if zf.Mode()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// moveDir moves src onto dst, falling back to a copy when they sit on
// different filesystems (temp and home can, in containers with mounted
// volumes).
func moveDir(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(dst, src)
		closeErr := dst.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

// handleSkillSample serves a downloadable example of the shape the upload
// endpoint expects. Generated in memory: it is documentation that executes,
// and keeping it next to the validator means the two cannot disagree.
func (s *Server) handleSkillSample(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := []struct {
		name, body string
	}{
		{"sample-skill/SKILL.md", `---
name: sample-skill
description: One sentence that tells the model when this skill applies. This field is required — it is the entire basis on which the model decides to load the skill.
---

# Sample skill

Instructions for the model go here, in plain markdown. Keep the body under
40KB; move detail into reference files and point at them.

- Point at scripts by relative path: scripts/hello.sh
- Point at references the same way: references/format.md
- The model reads files on demand, so listing what exists beats pasting it all here.
`},
		{"sample-skill/scripts/hello.sh", "#!/usr/bin/env bash\n# A script the skill's instructions can ask the model to run.\nset -euo pipefail\necho \"hello from sample-skill\"\n"},
		{"sample-skill/references/format.md", "# Reference\n\nLong-form material the model only needs sometimes lives here rather than in SKILL.md.\n"},
	}
	for _, f := range files {
		fw, err := zw.Create(f.name)
		if err == nil {
			_, err = fw.Write([]byte(f.body))
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="sample-skill.zip"`)
	w.Header().Set("Content-Length", fmt.Sprint(buf.Len()))
	_, _ = w.Write(buf.Bytes())
}

// decodeLenientJSON is the settings/session import decoder: unknown fields are
// ignored (forward compatibility for files written by a newer build) rather
// than rejected the way config.Load rejects them — an import that fails on an
// unknown field loses the user's data over a field they cannot act on.
func decodeLenientJSON(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	return dec.Decode(dst)
}
