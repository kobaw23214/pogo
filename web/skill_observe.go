package web

import (
	"context"
	"path/filepath"

	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

// Skill observation snapshots.
//
// Tool attribution starts from the skill set the loader registered to a
// session, never from a prompt or argv. Explicit /skill expansion uses the
// exact registered skill it actually reads from the current catalog. A
// session captures its tool snapshot at creation, so
// a skill upload that swaps the live set mid-conversation cannot re-attribute
// the older session's events: same tool instances, same identities, same
// read targets it was built with. Paths exist only to build identities and
// read lookup keys; they never enter an event.

// skillSnapshotEntry is one registered skill's observation record.
type skillSnapshotEntry struct {
	identity skillobserve.Identity
	// path is the canonical SKILL.md path, used solely as the read tool's
	// lookup key.
	path string
	// hidden marks disable-model-invocation skills: reachable explicitly,
	// but never exposed to the model, so never a catalog discovery.
	hidden bool
}

// canonicalSkillPath mirrors the skills loader's canonicalisation, so the
// identity hash and the read lookup agree with MatchRead and with the
// loader's own collision detection.
func canonicalSkillPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// skillIdentityOf resolves a registered skill's trusted identity. A skill
// without a SKILL.md path (or without a name) gets none, and none records
// nothing.
func skillIdentityOf(sk skills.Skill) skillobserve.Identity {
	if sk.Path == "" || sk.Name == "" {
		return skillobserve.Identity{}
	}
	identityName := sk.Name
	if sk.Source == "embedded" && sk.Name == "office" {
		for _, alias := range sk.Aliases {
			if alias == "docs" {
				identityName = "docs"
			}
		}
	}
	id := skillobserve.Identify(identityName, sk.Source, canonicalSkillPath(sk.Path))
	id.Name = sk.Name
	return id
}

// runtimeSkillIdentity projects a packaged runtime Skill into the identity
// inputs the bundle projection (BoundSession.Skills) declares: the package's
// skill-runtime:// location stands in for a filesystem path and the source is
// "bundle", so an execute event correlates by skill_id with the bundle's
// discovery/load events once those stages cover packaged Skills.
func runtimeSkillIdentity(skillID, skillName string) skillobserve.Identity {
	if skillID == "" || skillName == "" {
		return skillobserve.Identity{}
	}
	return skillobserve.Identify(skillName, "bundle", "skill-runtime://"+skillID+"/SKILL.md")
}

// skillReadTargets projects a snapshot into the narrow mapping the tool
// layer gets — the only bridge between skills knowledge and the registry.
func skillReadTargets(entries []skillSnapshotEntry) []tools.SkillReadTarget {
	if len(entries) == 0 {
		return nil
	}
	out := make([]tools.SkillReadTarget, 0, len(entries))
	for _, e := range entries {
		out = append(out, tools.SkillReadTarget{Identity: e.identity, Path: e.path})
	}
	return out
}

// recordSkillDiscovery emits one catalog event per model-visible skill of
// the session's captured snapshot, in the run's context, so every event
// carries the authoritative session/run/owner/trace of the run the catalog
// was exposed to. Hidden skills are not model-visible and record nothing;
// a run with no skills records nothing either.
func (s *Session) recordSkillDiscovery(ctx context.Context) {
	for _, e := range s.skillSnapshot {
		if e.hidden || e.identity.ID == "" {
			continue
		}
		_, span := skillobserve.Start(ctx, e.identity, "discovery", "catalog", "", "")
		span.End("ok", "")
	}
}

// loadSkillExplicit measures the actual instruction read. Its session and
// owner are authoritative local values; the run does not exist yet, so its
// ID stays empty. The eventual run shares the trace without pretending the
// instruction read spans the whole run.
func (s *Server) loadSkillExplicit(sess *Session, sk skills.Skill, extra string) (string, mcpobserve.Metadata, error) {
	return sess.loadSkillExplicit(sk, extra)
}

func (sess *Session) loadSkillExplicit(sk skills.Skill, extra string) (string, mcpobserve.Metadata, error) {
	md := mcpobserve.Metadata{SessionID: sess.ID, Entrypoint: "pogo"}
	sess.mu.Lock()
	if sess.owner != nil {
		md.OwnerID = sess.owner.SubjectID
	}
	sess.mu.Unlock()
	ctx := mcpobserve.WithMetadata(context.Background(), md)
	ctx = skillobserve.WithRecorder(ctx, sess.mgr.SkillRecorder())
	ctx = skillobserve.WithSource(ctx, "internal", "pogo")
	ctx, span := skillobserve.Start(ctx, skillIdentityOf(sk), "load", "explicit", "", "")
	defer span.End("interrupted", "unknown_outcome")
	expanded, err := skills.Invocation(sk, extra)
	if err != nil {
		span.End("error", "read_error")
	} else {
		span.End("ok", "")
	}
	md.TraceID = mcpobserve.MetadataFrom(ctx).TraceID
	return expanded, md, err
}
