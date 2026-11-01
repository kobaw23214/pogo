package sentinel

import (
	"reflect"
	"testing"
)

func TestSessionRecoveryViewsRetainRemovedAndHistoricalWorkersReadOnly(t *testing.T) {
	db, _ := testStore(t)
	w := createWatch(t, db)
	acceptFrame(t, db, w, oneFrame("frame", "key", 1))
	batch := claim(t, db, w.Spec.ID, 1)
	if batch == nil {
		t.Fatal("missing batch")
	}
	if err := db.SetBatchRun(batch.ID, "historical-worker", "old-run", "interrupted"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSession(w.Spec.ID, "current-worker"); err != nil {
		t.Fatal(err)
	}
	if err := db.Remove(w.Spec.ID); err != nil {
		t.Fatal(err)
	}
	before, err := db.SessionRecords()
	if err != nil || len(before) != 1 || before[0].Status != "removed" || before[0].Spec.SessionID != "current-worker" {
		t.Fatalf("removed session not retained: %+v %v", before, err)
	}
	for i := 0; i < 2; i++ {
		links, err := db.WorkerSessionLinks()
		if err != nil || links["historical-worker"] != w.Spec.ID {
			t.Fatalf("old batch worker was lost: %+v %v", links, err)
		}
		after, err := db.SessionRecords()
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("navigation changed native state: %+v %v", after, err)
		}
	}
	ordinary, err := db.ListAll()
	if err != nil || len(ordinary) != 0 {
		t.Fatal("recovery view changed supervisor semantics", err)
	}
}
