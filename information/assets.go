package information

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

func safeName(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune("<>:\"|?*/\\", r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.TrimLeft(name, "."))
	for len(name) > 160 {
		_, n := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-n]
	}
	if name == "" {
		return "file"
	}
	return name
}
func validAsset(a Asset) bool {
	m := assetShape.FindStringSubmatch(a.Asset)
	return len(m) == 3 && m[1] == a.SHA256 && m[2] == a.Name && a.Name == safeName(a.Name) && a.Size >= 0 && a.Size <= MaxAssetBytes && len(a.Mime) <= 255 && !strings.ContainsAny(a.Mime, "\r\n\x00")
}

// PutAsset streams and hashes one file. A content address never overwrites
// existing bytes. The caller retains an immutable handle after tree deletion.
func (s *Store) PutAsset(name, mimeType string, source io.Reader, max int64) (Asset, error) {
	if max <= 0 || max > MaxAssetBytes {
		max = MaxAssetBytes
	}
	name = safeName(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := os.OpenRoot(s.root)
	if err != nil {
		return Asset{}, err
	}
	defer r.Close()
	if err = s.verifyScope(r); err != nil {
		return Asset{}, err
	}
	if err = mkdir(r, "tmp"); err != nil {
		return Asset{}, err
	}
	token, err := newID()
	if err != nil {
		return Asset{}, err
	}
	tmp := "tmp/" + token
	f, err := r.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return Asset{}, err
	}
	defer r.Remove(tmp)
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(source, max+1))
	if err != nil || n > max {
		f.Close()
		if err == nil {
			err = ErrTooLarge
		}
		return Asset{}, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return Asset{}, err
	}
	sniff := make([]byte, 512)
	count, _ := f.Read(sniff)
	// MIME from bytes/extension is display metadata. HTML/SVG never become
	// same-origin active content in the HTTP reader.
	mimeType = mime.TypeByExtension(path.Ext(name))
	if mimeType == "" {
		mimeType = http.DetectContentType(sniff[:count])
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return Asset{}, err
	}
	if err = f.Close(); err != nil {
		return Asset{}, err
	}
	sha := hex.EncodeToString(h.Sum(nil))
	dir := "assets/" + sha
	if err = mkdir(r, dir); err != nil {
		return Asset{}, err
	}
	asset := Asset{Name: name, Mime: mimeType, Size: n, SHA256: sha, Asset: "information-assets/" + sha + "/" + name}
	if _, err = r.Lstat(dir + "/data"); errors.Is(err, os.ErrNotExist) {
		if _, metaErr := r.Lstat(dir + "/meta.json"); errors.Is(metaErr, os.ErrNotExist) {
			if err = atomicJSON(r, dir+"/meta.json", asset, true); err != nil {
				return Asset{}, err
			}
		} else if metaErr != nil {
			return Asset{}, metaErr
		}
		if err = r.Rename(tmp, dir+"/data"); err != nil {
			return Asset{}, err
		}
	} else if err != nil {
		return Asset{}, err
	}
	check, _, err := s.assetRead(r, asset.Asset)
	if err != nil {
		return Asset{}, err
	}
	check.Close()
	return asset, nil
}
func (s *Store) assetRead(r *os.Root, handle string) (*os.File, Asset, error) {
	if err := s.verifyScope(r); err != nil {
		return nil, Asset{}, err
	}
	m := assetShape.FindStringSubmatch(handle)
	if len(m) != 3 || m[2] != safeName(m[2]) {
		return nil, Asset{}, ErrInvalid
	}
	var a Asset
	dir := "assets/" + m[1]
	if err := decode(r, dir+"/meta.json", 4096, &a); err != nil {
		return nil, Asset{}, err
	}
	if !validAsset(a) || a.SHA256 != m[1] {
		return nil, Asset{}, ErrInvalid
	}
	f, err := regular(r, dir+"/data", MaxAssetBytes)
	if err != nil {
		return nil, Asset{}, err
	}
	st, err := f.Stat()
	if err != nil || st.Size() != a.Size {
		f.Close()
		return nil, Asset{}, ErrInvalid
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		f.Close()
		return nil, Asset{}, ErrInvalid
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, Asset{}, err
	}
	a.Name = m[2]
	a.Asset = handle
	if byExt := mime.TypeByExtension(path.Ext(a.Name)); byExt != "" {
		a.Mime = byExt
	}
	return f, a, nil
}
func (s *Store) AssetRead(handle string) (*os.File, Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, Asset{}, err
	}
	defer r.Close()
	return s.assetRead(r, handle)
}

// MaterializeAsset returns an independently editable workspace-relative copy.
// An unchanged copy in the same trusted workspace may be reused. Callers which
// stage several assets together should use BeginMaterialization instead;
// they must never remove a returned public path after another asset fails.
func (s *Store) MaterializeAsset(handle, workspace string) (string, Asset, error) {
	batch, err := s.BeginMaterialization(workspace)
	if err != nil {
		return "", Asset{}, err
	}
	defer batch.Rollback()
	result, err := batch.MaterializeAsset(handle)
	if err != nil {
		return "", Asset{}, err
	}
	if err = batch.Commit(); err != nil {
		return "", Asset{}, err
	}
	return result.Path, result.Asset, nil
}
