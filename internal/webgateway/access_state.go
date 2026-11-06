package webgateway

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/yosukeno/pogo/webservice"
)

// Only explicit access revocations need a version. Ordinary opens, renewals
// and process generations retain a URL derived from the durable lease ID.
type leaseAccessVersion struct {
	Version   uint64    `json:"version"`
	ExpiresAt time.Time `json:"expires_at"`
}

type accessState struct {
	Version int                           `json:"version"`
	Secret  string                        `json:"secret"`
	Leases  map[string]leaseAccessVersion `json:"leases"`
}

func (r *Router) loadAccessState() error {
	r.leaseAccess = map[string]leaseAccessVersion{}
	if r.opts.StatePath == "" {
		_, err := rand.Read(r.grantSecret[:])
		return err
	}
	if !filepath.IsAbs(r.opts.StatePath) {
		return errors.New("gateway: access state must use an absolute private path")
	}
	if err := privateSocketParent(r.opts.StatePath); err != nil {
		return err
	}
	info, err := os.Lstat(r.opts.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := rand.Read(r.grantSecret[:]); err != nil {
			return err
		}
		// Link the completed private file without replacing another gateway's
		// key when two launchers initialize the same installation concurrently.
		if err := r.writeAccessStateLocked(r.leaseAccess, true); errors.Is(err, os.ErrExist) {
			return r.loadAccessState()
		} else {
			return err
		}
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16<<20 {
		return errors.New("gateway: access state must be a bounded private regular file")
	}
	if err := socketOwnedByProcess(info); err != nil {
		return err
	}
	f, err := os.Open(r.opts.StatePath)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 16<<20))
	decoder.DisallowUnknownFields()
	var state accessState
	if err := decoder.Decode(&state); err != nil {
		return errors.New("gateway: invalid access state")
	}
	if decoder.Decode(new(any)) != io.EOF || state.Version != 1 || state.Leases == nil {
		return errors.New("gateway: invalid access state")
	}
	secret, err := hex.DecodeString(state.Secret)
	if err != nil || len(secret) != len(r.grantSecret) {
		return errors.New("gateway: invalid access state signing key")
	}
	copy(r.grantSecret[:], secret)
	for key, v := range state.Leases {
		decoded, err := hex.DecodeString(key)
		if err != nil || len(decoded) != 32 || v.Version == 0 || v.ExpiresAt.IsZero() {
			return errors.New("gateway: invalid access state lease")
		}
		// The Manager may have persisted a renewal that this gateway never saw.
		// Expiry here is only the last published deadline, not proof that the
		// lease can never return. Forgetting a rotation would revive version 0.
		r.leaseAccess[key] = v
	}
	return nil
}

func (r *Router) saveAccessStateLocked(leases map[string]leaseAccessVersion) error {
	return r.writeAccessStateLocked(leases, false)
}

func (r *Router) writeAccessStateLocked(leases map[string]leaseAccessVersion, exclusive bool) error {
	if r.opts.StatePath == "" {
		return nil
	}
	state := accessState{Version: 1, Secret: hex.EncodeToString(r.grantSecret[:]), Leases: leases}
	data, err := json.Marshal(state)
	if err != nil || len(data) > 16<<20 {
		return errors.New("gateway: access state exceeds its storage budget")
	}
	f, err := os.CreateTemp(filepath.Dir(r.opts.StatePath), ".gateway-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if exclusive {
		return os.Link(f.Name(), r.opts.StatePath)
	}
	return os.Rename(f.Name(), r.opts.StatePath)
}

func leaseAccessKey(rec webservice.Record) string {
	data, _ := json.Marshal([]string{rec.Installation, rec.Owner, rec.Session, rec.ServiceID, rec.LeaseID})
	key := sha256.Sum256(data)
	return hex.EncodeToString(key[:])
}

func (r *Router) leaseToken(rec webservice.Record) (string, string) {
	key := leaseAccessKey(rec)
	version := r.leaseAccess[key].Version
	sign := func(kind string, count int) string {
		h := hmac.New(sha256.New, r.grantSecret[:])
		_, _ = h.Write([]byte(kind + "\x00" + key + "\x00" + strconv.FormatUint(version, 10)))
		return hex.EncodeToString(h.Sum(nil)[:count])
	}
	return sign("website-url", 24), sign("website-access-id", 16)
}

func (r *Router) installLeaseAccessLocked(rec webservice.Record) error {
	key := leaseAccessKey(rec)
	if old, exists := r.leaseAccess[key]; exists && !old.ExpiresAt.Equal(rec.LeaseExpiresAt) {
		copy := r.copyAccessVersions()
		old.ExpiresAt = rec.LeaseExpiresAt
		copy[key] = old
		if err := r.saveAccessStateLocked(copy); err != nil {
			return err
		}
		r.leaseAccess = copy
	}
	token, id := r.leaseToken(rec)
	hash := sha256.Sum256([]byte(token))
	if old := r.grants[hash]; old != nil {
		// Renewals update the same grant in place, preserving live requests.
		old.generation = rec.Generation
		old.expires = rec.LeaseExpiresAt
		return nil
	}
	r.grants[hash] = &accessGrant{id: id, service: rec.ServiceID, owner: rec.Owner, session: rec.Session, generation: rec.Generation, expires: rec.LeaseExpiresAt, active: map[*activeRequest]struct{}{}}
	r.grantIDs[id] = hash
	return nil
}

func (r *Router) copyAccessVersions() map[string]leaseAccessVersion {
	copy := make(map[string]leaseAccessVersion, len(r.leaseAccess)+1)
	for key, value := range r.leaseAccess {
		copy[key] = value
	}
	return copy
}

func (r *Router) rotateLeaseAccessLocked(rec webservice.Record) error {
	key := leaseAccessKey(rec)
	current := r.leaseAccess[key]
	if current.Version == ^uint64(0) {
		return webservice.Fail("ACCESS_ROTATION_UNAVAILABLE", "website access version exhausted")
	}
	copy := r.copyAccessVersions()
	copy[key] = leaseAccessVersion{Version: current.Version + 1, ExpiresAt: rec.LeaseExpiresAt}
	if err := r.saveAccessStateLocked(copy); err != nil {
		return webservice.Fail("ACCESS_ROTATION_UNAVAILABLE", "website access revocation could not be persisted")
	}
	r.leaseAccess = copy
	r.revokeServiceLocked(rec.ServiceID, 0)
	return r.installLeaseAccessLocked(rec)
}
