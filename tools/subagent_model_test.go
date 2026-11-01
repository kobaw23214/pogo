package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestSubagentParentModelUpdatesInheritedDefaultsOnly(t *testing.T) {
	for _, tc := range []struct {
		name, explicit string
		pool           []ExploreTarget
		want           string
	}{
		{name: "inherited", want: "parent-new"},
		{name: "explicit", explicit: "specialist", want: "specialist"},
		{name: "explicit equal to old parent", explicit: "parent-old", want: "parent-old"},
		{name: "pool", pool: []ExploreTarget{{Model: "pool-model", Provider: "provider"}}, want: "pool-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Subagent{Model: "parent-old", ExploreModel: tc.explicit, ExplorePool: tc.pool}
			s.SetParentModel("parent-new")
			model, provider := s.pickDelegationModel(ModeExplore)
			defer s.releaseExplore(provider)
			if model != tc.want {
				t.Fatalf("explore default=%q want %q", model, tc.want)
			}
			if model, _ := s.pickDelegationModel(ModeEdit); model != "parent-new" {
				t.Fatalf("edit default=%q", model)
			}
		})
	}
}

func TestSubagentParentModelSnapshotsAreRaceSafe(t *testing.T) {
	s := &Subagent{Model: "parent-a"}
	var wg sync.WaitGroup
	wg.Add(5)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			if i%2 == 0 {
				s.SetParentModel("parent-b")
			} else {
				s.SetParentModel("parent-a")
			}
		}
	}()
	for i := 0; i < 4; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 2000; j++ {
				model, provider := s.pickDelegationModel(ModeExplore)
				s.releaseExplore(provider)
				if model != "parent-a" && model != "parent-b" {
					t.Errorf("torn model snapshot %q", model)
					return
				}
				if model := s.parentModel(); model != "parent-a" && model != "parent-b" {
					t.Errorf("torn parent snapshot %q", model)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestSubagentQueuedBackgroundKeepsDispatchedModel(t *testing.T) {
	t.Setenv("STUB_MODE", "prompt")
	root, stub := repoFixture(t)
	cap := newBgCapture()
	bg := NewBgRunner(context.Background(), cap.hooks())
	t.Cleanup(bg.Close)
	s := &Subagent{Cwd: root, Exe: stub, Model: "parent-old", Concurrency: 1, Bg: bg}
	if err := s.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Keep the actual child process queued while the parent changes models.
	if _, err := call(t, s, ModeExplore, "accepted before model update"); err != nil {
		s.release()
		t.Fatal(err)
	}
	s.SetParentModel("parent-new")
	s.release()
	first := cap.awaitComplete(t)
	if first.Err != nil || first.Details.Model != "parent-old" || !strings.Contains(first.Text, "model=parent-old") {
		t.Fatalf("queued child's model changed: %+v", first)
	}
	if _, err := call(t, s, ModeExplore, "accepted after model update"); err != nil {
		t.Fatal(err)
	}
	second := cap.awaitComplete(t)
	if second.Err != nil || second.Details.Model != "parent-new" || !strings.Contains(second.Text, "model=parent-new") {
		t.Fatalf("new child retained stale defaults: %+v", second)
	}
}
