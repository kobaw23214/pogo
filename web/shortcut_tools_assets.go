package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yosukeno/pogo/information"
	"github.com/yosukeno/pogo/inputref"
)

type shortcutToolAssetPlan struct {
	id, handle string
	source     *os.File
	record     starterAssetRecord
}

// A transaction plans paths without writing bytes. Only a completely compiled
// and authorized tree is staged, and only newly allocated directories are ever
// rolled back. Existing private snapshots and source files are never replaced.
type shortcutToolAssetBatch struct {
	b         *shortcutToolBackend
	plans     map[string]*shortcutToolAssetPlan
	root      *os.Root
	created   []string
	committed bool
}

type shortcutToolContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r shortcutToolContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(data)
}

// Only selected mutable workspace references need a byte witness. Immutable
// library/private snapshots already carry a verified content hash. Search does
// not copy any file or read arbitrary model-supplied paths.
func (b *shortcutToolBackend) freezeMessageFiles(ctx context.Context, ref shortcutToolReference) (shortcutToolReference, error) {
	if ref.origin != "message" {
		return ref, nil
	}
	witnesses := map[string]starterAssetRecord{}
	for _, part := range flatInputParts(ref.parts) {
		if part.Type != "file" || strings.HasPrefix(part.Path, "shortcut-assets/") {
			continue
		}
		if _, ok := witnesses[part.Path]; ok {
			continue
		}
		if strings.HasPrefix(part.Path, "starter-assets/") || strings.HasPrefix(part.Path, "information-assets/") {
			return ref, errShortcutAssetInvalid
		}
		root, err := os.OpenRoot(b.workspace)
		if err != nil {
			return ref, err
		}
		file, err := root.Open(part.Path)
		_ = root.Close()
		if err != nil {
			return ref, errors.New("message file is no longer available; attach or select it again")
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > int64(attachMaxMB)<<20 {
			_ = file.Close()
			return ref, errShortcutAssetInvalid
		}
		size, hash, err := copyStarterFile(io.Discard, shortcutToolContextReader{ctx, file}, info.Size())
		_ = file.Close()
		if err != nil || size != info.Size() {
			return ref, errors.New("message file changed while reading; search again")
		}
		witnesses[part.Path] = starterAssetRecord{Name: strings.ToValidUTF8(sanitizeName(path.Base(part.Path)), ""), Size: size, SHA256: hash}
	}
	if len(witnesses) > 0 {
		ref.fileWitnesses = witnesses
		sum := shortcutToolDigest(witnesses)
		ref.summary.RefID = "sr_" + shortcutToolDigest(ref.summary.RefID+":"+sum)
		ref.summary.Version = ref.summary.Version + ":" + sum
	}
	return ref, nil
}

func shortcutToolAssetMatches(ctx context.Context, root *os.Root, handle string, record starterAssetRecord) bool {
	file, meta, err := readStarterAsset(root, handle)
	if err != nil {
		return false
	}
	defer file.Close()
	if meta.Name != record.Name || meta.Size != record.Size || meta.SHA256 != record.SHA256 {
		return false
	}
	hash := sha256.New()
	_, err = io.Copy(hash, shortcutToolContextReader{ctx, file})
	return err == nil && hex.EncodeToString(hash.Sum(nil)) == record.SHA256
}

// Reuse is confined to this owner's Agent namespace and checks actual bytes,
// not merely a matching filename. A modified/corrupt previous copy is ignored
// and preserved. No existing asset is ever a rollback candidate.
func (t *shortcutToolAssetBatch) reusable(ctx context.Context, record starterAssetRecord) string {
	if record.SHA256 == "" {
		return ""
	}
	namespace, err := t.b.m.shortcutAssetNamespace(t.b.owner, t.b.agentID)
	if err != nil {
		return ""
	}
	root, err := os.OpenRoot(namespace)
	if err != nil {
		return ""
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return ""
	}
	defer dir.Close()
	names, err := dir.Readdirnames(4096)
	if err != nil && err != io.EOF {
		return ""
	}
	sort.Strings(names)
	for _, id := range names {
		if ctx.Err() != nil {
			return ""
		}
		if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
			continue
		}
		handle := path.Join("shortcut-assets", id, record.Name)
		if shortcutToolAssetMatches(ctx, root, handle, record) {
			return handle
		}
	}
	return ""
}

func (t *shortcutToolAssetBatch) close() {
	for _, plan := range t.plans {
		if plan.source != nil {
			_ = plan.source.Close()
		}
	}
	if t.root != nil {
		if !t.committed {
			for _, id := range t.created {
				_ = t.root.RemoveAll(id)
			}
		}
		_ = t.root.Close()
	}
}

func (t *shortcutToolAssetBatch) prepare(ctx context.Context, ref shortcutToolReference) ([]inputref.Part, error) {
	parts := inputref.Clone(ref.parts)
	for _, part := range inputPartPointers(parts) {
		if part.Type != "file" {
			continue
		}
		key := ref.origin + ":" + part.Path
		if plan, ok := t.plans[key]; ok {
			part.Path = plan.handle
			continue
		}
		if strings.HasPrefix(part.Path, "shortcut-assets/") {
			namespace, err := t.b.m.shortcutAssetNamespace(t.b.owner, t.b.agentID)
			if err != nil {
				return nil, err
			}
			root, err := os.OpenRoot(namespace)
			if err != nil {
				return nil, errShortcutAssetInvalid
			}
			file, record, err := readStarterAsset(root, part.Path)
			_ = root.Close()
			if err != nil {
				return nil, errShortcutAssetInvalid
			}
			hash := sha256.New()
			_, err = io.Copy(hash, shortcutToolContextReader{ctx, file})
			_ = file.Close()
			if err != nil || hex.EncodeToString(hash.Sum(nil)) != record.SHA256 {
				return nil, errShortcutAssetInvalid
			}
			t.plans[key] = &shortcutToolAssetPlan{handle: part.Path}
			continue
		}
		var file *os.File
		var record starterAssetRecord
		var err error
		switch {
		case ref.origin == "information" && strings.HasPrefix(part.Path, "information-assets/"):
			store, e := t.b.m.InformationStore(t.b.owner)
			if e != nil {
				return nil, e
			}
			var asset information.Asset
			file, asset, err = store.AssetRead(part.Path)
			if err == nil {
				record = starterAssetRecord{Name: asset.Name, Size: asset.Size, SHA256: asset.SHA256}
			}
		case ref.origin == "quick_card" && strings.HasPrefix(part.Path, "starter-assets/"):
			key := sha256.Sum256([]byte("agent:" + t.b.agentID))
			namespace := filepath.Join(t.b.m.cfg.SessionDir, ".starter-assets", hex.EncodeToString(key[:]))
			root, e := os.OpenRoot(namespace)
			if e != nil {
				return nil, errShortcutAssetInvalid
			}
			file, record, err = readStarterAsset(root, part.Path)
			_ = root.Close()
		case ref.origin == "message" && !strings.HasPrefix(part.Path, "starter-assets/") && !strings.HasPrefix(part.Path, "information-assets/"):
			root, e := os.OpenRoot(t.b.workspace)
			if e != nil {
				return nil, e
			}
			file, err = root.Open(part.Path)
			_ = root.Close()
			if err == nil {
				info, e := file.Stat()
				err = e
				if err == nil {
					record = starterAssetRecord{Name: strings.ToValidUTF8(sanitizeName(path.Base(part.Path)), ""), Size: info.Size()}
					if !info.Mode().IsRegular() {
						err = errShortcutAssetInvalid
					}
					if witness, ok := ref.fileWitnesses[part.Path]; ok {
						if witness.Size != info.Size() {
							err = errors.New("message file changed since it was referenced; search again")
						}
						record = witness
					} else {
						err = errors.New("message file has no trusted byte witness; search again")
					}
				}
			}
		default:
			return nil, errShortcutAssetInvalid
		}
		plan := t.plans[key]
		if plan == nil {
			plan = &shortcutToolAssetPlan{source: file, record: record}
			t.plans[key] = plan
		}
		if err != nil || plan.source == nil || plan.record.Size <= 0 || plan.record.Size > int64(attachMaxMB)<<20 || plan.record.Name == "" {
			return nil, errShortcutAssetInvalid
		}
		// Verify the mutable source even if an old private snapshot can be
		// reused. A stale handle must not silently substitute newer file bytes.
		if ref.origin == "message" {
			size, hash, e := copyStarterFile(io.Discard, shortcutToolContextReader{ctx, plan.source}, plan.record.Size)
			if e != nil || size != plan.record.Size || hash != plan.record.SHA256 {
				return nil, errors.New("message file changed since it was referenced; search again")
			}
			if _, e = plan.source.Seek(0, io.SeekStart); e != nil {
				return nil, e
			}
		}
		if reused := t.reusable(ctx, plan.record); reused != "" {
			_ = plan.source.Close()
			plan.source = nil
			plan.handle = reused
			part.Path = reused
			continue
		}
		plan.id = randomID(16)
		plan.handle = path.Join("shortcut-assets", plan.id, plan.record.Name)
		part.Path = plan.handle
	}
	return parts, nil
}

func (t *shortcutToolAssetBatch) stage(ctx context.Context) error {
	for _, plan := range t.plans {
		if plan.source == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if t.root == nil {
			namespace, err := t.b.m.shortcutAssetNamespace(t.b.owner, t.b.agentID)
			if err != nil {
				return err
			}
			t.root, err = openStarterAssetRoot(namespace)
			if err != nil {
				return err
			}
		}
		if err := t.root.Mkdir(plan.id, 0700); err != nil {
			return err
		}
		t.created = append(t.created, plan.id)
		file, err := t.root.OpenFile(plan.id+"/data", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		size, hash, err := copyStarterFile(file, shortcutToolContextReader{ctx, plan.source}, plan.record.Size)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil || closeErr != nil || size != plan.record.Size || plan.record.SHA256 != "" && hash != plan.record.SHA256 {
			return errors.New("reference file changed or could not be copied; no shortcut saved")
		}
		plan.record.SHA256 = hash
		raw, _ := json.Marshal(plan.record)
		if err = t.root.WriteFile(plan.id+"/meta.json", raw, 0600); err != nil {
			return err
		}
	}
	return nil
}
