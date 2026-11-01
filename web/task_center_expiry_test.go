package web

import (
	"testing"
	"time"

	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/sentinel"
)

func TestTaskCenterProjectsPersistedDeadlinesAndNoExpiredNextRun(t *testing.T) {
	h, _ := newStudioHarness(t, nil)
	now := time.Now()
	s := &Server{mgr: h.mgr}
	deadline := now.Add(time.Hour)
	j := schedule.Job{ID: "deadline", Kind: schedule.KindEvery, EverySec: 120, CreatedAt: now, ExpiresAt: deadline.Format(time.RFC3339Nano), Enabled: true}
	v := s.taskCenterScheduleView(j)
	if v.ExpiresAt != deadline.UnixMilli() {
		t.Fatalf("schedule deadline lost: %+v", v)
	}
	j.ExpiresAt = now.Add(-time.Second).Format(time.RFC3339Nano)
	v = s.taskCenterScheduleView(j)
	if v.State != "expired" || v.NextRun != "" {
		t.Fatalf("expired schedule still has a next run: %+v", v)
	}
	w := sentinel.Watch{Spec: sentinel.Spec{ID: "expired-watch", ExpiresAt: now.Add(-time.Second).UnixMilli(), Enabled: false}, Pending: 3, NextAt: now.Add(time.Hour).UnixMilli()}
	v = s.taskCenterSentinelView(w)
	if v.State != "expired" || v.ExpiresAt != w.Spec.ExpiresAt || v.NextRun != "" || v.PendingEvents != 3 || !v.Attention {
		t.Fatalf("expired watch was relabelled paused or lost pending work: %+v", v)
	}
}
