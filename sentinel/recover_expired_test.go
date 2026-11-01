package sentinel

import (
	"reflect"
	"testing"
)

func TestRecoverKeepsExpiredTransitionIdempotentAndObservationsIntact(t *testing.T) {
	db, _ := testStore(t)
	db.clock = func() int64 { return 1000 }
	spec := testSpec(t)
	spec.ExpiresAt = 2000
	w, err := db.Create(spec)
	if err != nil {
		t.Fatal(err)
	}
	acceptFrame(t, db, w, oneFrame("before-expiry", "accepted-key", 1))
	expired, changed, err := db.ExpireAt(w.Spec.ID, w.Spec.Generation, spec.ExpiresAt)
	if err != nil || !changed || expired.Status != "expired" || expired.Spec.Enabled {
		t.Fatalf("initial deadline transition: %+v changed=%v %v", expired, changed, err)
	}
	for restart := 0; restart < 3; restart++ {
		if err := db.Recover(); err != nil {
			t.Fatal(err)
		}
		recovered, err := db.Get(w.Spec.ID)
		if err != nil || recovered.Status != "expired" || recovered.Spec.Generation != expired.Spec.Generation+int64(restart+1) {
			t.Fatalf("restart %d lost terminal state/fence: %+v %v", restart, recovered, err)
		}
		reconciled, changed, err := db.ExpireAt(w.Spec.ID, recovered.Spec.Generation, spec.ExpiresAt+1000)
		if err != nil || changed {
			t.Fatalf("restart %d repeated expiration: changed=%v %v", restart, changed, err)
		}
		expected := expired.Spec
		expected.Generation, expected.UpdatedAt = reconciled.Spec.Generation, reconciled.Spec.UpdatedAt
		if !reflect.DeepEqual(expected, reconciled.Spec) || !reflect.DeepEqual(expired.State, reconciled.State) || !reflect.DeepEqual(expired.Cursor, reconciled.Cursor) || reconciled.Pending != expired.Pending {
			t.Fatalf("restart %d changed business definition or accepted progress", restart)
		}
		var frames, events int
		if err := db.db.QueryRow("SELECT count(*) FROM sentinel_frames WHERE watch_id=?", w.Spec.ID).Scan(&frames); err != nil {
			t.Fatal(err)
		}
		if err := db.db.QueryRow("SELECT count(*) FROM sentinel_events WHERE watch_id=?", w.Spec.ID).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if frames != 1 || events != 1 {
			t.Fatal("recovery changed accepted observations")
		}
	}
}
