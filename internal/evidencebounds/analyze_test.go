package evidencebounds

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

var testDomain = Domain{ResultID: "R-test", Kind: "stored_text"}

func full(text string) Fragment {
	return Fragment{ID: "body", Domain: testDomain, Start: 0, End: len(text), Text: text, StartsLine: true, EndsLine: true}
}

func search(id, text, pattern string, ordinals ...int) Search {
	all := []Hit{}
	for at := 0; at <= len(text)-len(pattern); {
		next := strings.Index(text[at:], pattern)
		if next < 0 {
			break
		}
		start := at + next
		all = append(all, Hit{len(all), start, start + len(pattern)})
		at = start + len(pattern)
	}
	s := Search{ID: id, Domain: testDomain, Pattern: pattern, Total: len(all), ScanComplete: true}
	for _, ordinal := range ordinals {
		s.Hits = append(s.Hits, all[ordinal])
	}
	return s
}

func coverage(t *testing.T, r Report, id string) Coverage {
	t.Helper()
	for _, c := range r.Coverage {
		for _, alias := range c.IDs {
			if alias == id {
				return c
			}
		}
	}
	t.Fatal("selector missing", id)
	return Coverage{}
}

func TestIndependentBodyFindsRemainderCounterexampleWithoutClosingPageGap(t *testing.T) {
	text := "ERROR unrelated\nINFO target\nERROR target\n"
	left := search("errors", text, "ERROR", 0)
	right := search("target", text, "target", 0, 1)
	r := Analyze(Input{Fragments: []Fragment{full(text)}, Searches: []Search{left, right}, Relations: []RelationRequest{{"errors", "target"}}})
	c := coverage(t, r, "errors")
	if c.NativeDelivered != 1 || c.Total != 2 || c.OtherBodyCount != 1 || len(c.OtherBody) != 1 || c.NativeComplete || !c.CompleteBody {
		t.Fatalf("other body changed pagination or failed to preserve knowledge: %+v", c)
	}
	if len(r.Relations) != 1 || r.Relations[0].Kind != "counterexample" || len(r.Relations[0].Witnesses) != 1 {
		t.Fatalf("complete target scan hid its tail counterexample: %+v", r)
	}
	w := r.Relations[0].Witnesses[0]
	if w.Left.LineStart != w.Right.LineStart || w.Left.Ordinal != nil || w.Left.OrdinalLower == nil || *w.Left.OrdinalLower != 1 || w.Left.LineSHA256 != hash("ERROR target\n") {
		t.Fatalf("counterexample invented an ordinal or lost actual complete-line proof: %+v", w)
	}
}

func TestSameSelectorLaterPageUnionsBeforeOtherBodyAndDisjointProof(t *testing.T) {
	text := "ERROR unrelated\nERROR unrelated\nERROR target\n"
	first := search("page0", text, "ERROR", 0)
	last := search("page2", text, "ERROR", 2)
	r := Analyze(Input{Fragments: []Fragment{full(text)}, Searches: []Search{first, last, search("target", text, "target", 0)}, Relations: []RelationRequest{{"page0", "target"}}})
	c := coverage(t, r, "page0")
	if c.NativeDelivered != 2 || c.Total != 3 || c.OtherBodyCount != 1 || len(c.IDs) != 2 || c.OtherBody[0].Start != strings.Index(text, "ERROR unrelated\nERROR")+len("ERROR unrelated\n") {
		t.Fatalf("same-selector continuation became other-body delivery: %+v", c)
	}
	if r.Relations[0].Kind != "verified_disjoint" {
		t.Fatalf("the only target match was already returned by this selector: %+v", r.Relations[0])
	}
}

func TestMultilineContextIsNotSameLineCooccurrence(t *testing.T) {
	text := "ERROR left\nINFO target\n"
	left := search("left", text, "ERROR")
	right := search("right", text, "target", 0)
	r := Analyze(Input{Fragments: []Fragment{full(text)}, Searches: []Search{left, right}, Relations: []RelationRequest{{"left", "right"}}})
	if r.Relations[0].Kind != "verified_disjoint" {
		t.Fatalf("different context lines became cooccurrence: %+v", r)
	}
	right.Total = 2
	r = Analyze(Input{Fragments: []Fragment{full(text)}, Searches: []Search{left, right}, Relations: []RelationRequest{{"left", "right"}}})
	if r.Relations[0].Kind != "unknown" {
		t.Fatal("partial right selection established exclusion")
	}
}

func TestOnlyActualCompleteAdjacentBytesCanEstablishLine(t *testing.T) {
	text := "ERROR target\n"
	left, right := search("left", text, "ERROR"), search("right", text, "target", 0)
	first := Fragment{ID: "a", Domain: testDomain, Start: 0, End: 6, Text: text[:6], StartsLine: true}
	last := Fragment{ID: "b", Domain: testDomain, Start: 7, End: len(text), Text: text[7:], EndsLine: true}
	in := Input{Fragments: []Fragment{first, last}, Searches: []Search{left, right}, Relations: []RelationRequest{{"left", "right"}}}
	if r := Analyze(in); r.Relations[0].Kind != "unknown" || coverage(t, r, "left").OtherBodyCount != 0 {
		t.Fatalf("one missing byte was filled: %+v", r)
	}
	last.Start, last.Text = 6, text[6:]
	in.Fragments[1] = last
	r := Analyze(in)
	if r.Relations[0].Kind != "counterexample" || len(r.Relations[0].Witnesses[0].Left.FragmentIDs) != 2 {
		t.Fatalf("real adjacent bytes did not establish the full line: %+v", r)
	}
	in.Incomplete = true
	r = Analyze(in)
	if r.Relations[0].Kind != "unknown" || len(r.Relations[0].Witnesses) == 0 || !coverage(t, r, "left").OtherBodyCountLowerBound {
		t.Fatalf("incomplete caller lost its witness or invented complete proof: %+v", r)
	}
}

func TestSelfOverlapAndDomainMismatchCannotInventGlobalMatches(t *testing.T) {
	text := "aaa target\n"
	left, right := search("left", text, "aa"), search("right", text, "target", 0)
	r := Analyze(Input{Fragments: []Fragment{full(text)}, Searches: []Search{left, right}, Relations: []RelationRequest{{"left", "right"}}})
	if coverage(t, r, "left").OtherBodyCount != 0 || r.Relations[0].Kind != "unknown" {
		t.Fatalf("local self-overlap supplied a global hit: %+v", r)
	}
	left = search("left", text, "aaa")
	right.Domain.Pointer = "/other"
	r = Analyze(Input{Fragments: []Fragment{full(text)}, Searches: []Search{left, right}, Relations: []RelationRequest{{"left", "right"}}})
	if r.Relations[0].Kind != "unknown" {
		t.Fatal("different pointer/domain established a relation")
	}
}

func TestConflictsAndIntegerWrapRemainUnknown(t *testing.T) {
	text := "x target\n"
	left, right := search("left", text, "x"), search("right", text, "target", 0)
	conflict := full(text)
	conflict.Text = "y target\n"
	r := Analyze(Input{Fragments: []Fragment{full(text), conflict}, Searches: []Search{left, right}, Relations: []RelationRequest{{"left", "right"}}})
	if r.Relations[0].Kind != "unknown" || !r.Incomplete {
		t.Fatal("ambiguous fragment ID remained trusted")
	}
	conflict = full(text)
	conflict.Domain.Kind = "json"
	r = Analyze(Input{Fragments: []Fragment{full(text), conflict}, Searches: []Search{left, right}, Relations: []RelationRequest{{"left", "right"}}})
	if r.Relations[0].Kind != "unknown" {
		t.Fatal("fragment ID collision across domains remained trusted")
	}
	maxInt := int(^uint(0) >> 1)
	left.Hits = []Hit{{Ordinal: 0, Start: maxInt, End: -maxInt - 1}}
	r = Analyze(Input{Fragments: []Fragment{full(text)}, Searches: []Search{left, right}, Relations: []RelationRequest{{"left", "right"}}})
	if r.Relations[0].Kind != "unknown" {
		t.Fatal("overflowing native range remained trusted")
	}
	conflict = full(text)
	conflict.Text = "y target\n"
	conflict.ID = "different-id"
	r = Analyze(Input{Fragments: []Fragment{full(text), conflict}, Searches: []Search{search("left", text, "x"), right}, Relations: []RelationRequest{{"left", "right"}}})
	if r.Relations[0].Kind != "unknown" {
		t.Fatal("conflicting overlap supplied a body")
	}
}

func TestWitnessSamplesAreNotTheKnownTotalAndRepeatedInputIsStable(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "event marker=%d\n", i)
	}
	text := b.String()
	in := Input{Fragments: []Fragment{full(text), full(text)}, Searches: []Search{search("events", text, "event", 0)}}
	r := Analyze(in)
	c := coverage(t, r, "events")
	if c.OtherBodyCount != 11 || len(c.OtherBody) != MaxWitnesses || c.OtherBodyCountLowerBound || !c.CompleteBody {
		t.Fatalf("sample count was confused with all observed matches: %+v", c)
	}
	for i := 0; i < 5; i++ {
		other := Analyze(in)
		if !reflect.DeepEqual(other, r) {
			t.Fatal("deterministic actual input changed receipts")
		}
	}
}
