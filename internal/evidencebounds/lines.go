package evidencebounds

import (
	"sort"
	"strings"
)

type segment struct {
	start, end           int
	text                 string
	startsLine, endsLine bool
	fragments            []Fragment
}

func mergeLines(fragments []Fragment) ([]line, string) {
	ordered := append([]Fragment(nil), fragments...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Start == ordered[j].Start {
			return ordered[i].End < ordered[j].End
		}
		return ordered[i].Start < ordered[j].Start
	})
	var merged []segment
	for _, f := range ordered {
		if f.Start == f.End {
			continue
		}
		if len(merged) == 0 || f.Start > merged[len(merged)-1].end {
			merged = append(merged, segment{f.Start, f.End, f.Text, f.StartsLine || f.Start == 0, f.EndsLine, []Fragment{f}})
			continue
		}
		last := &merged[len(merged)-1]
		overlap := min(last.end, f.End) - f.Start
		if overlap > 0 && last.text[f.Start-last.start:f.Start-last.start+overlap] != f.Text[:overlap] {
			return nil, "conflicting_fragment_overlap"
		}
		if f.Start == last.start {
			last.startsLine = last.startsLine || f.StartsLine || f.Start == 0
		}
		if f.End > last.end {
			last.text += f.Text[last.end-f.Start:]
			last.end, last.endsLine = f.End, f.EndsLine
		} else if f.End == last.end {
			last.endsLine = last.endsLine || f.EndsLine
		}
		last.fragments = append(last.fragments, f)
	}
	var lines []line
	reason := ""
	for _, s := range merged {
		at := 0
		refIndex := 0
		var active []Fragment
		if !s.startsLine {
			newline := strings.IndexByte(s.text, '\n')
			if newline < 0 {
				continue
			}
			at = newline + 1 // The first partial line is not a complete body witness.
		}
		for at < len(s.text) {
			if len(lines) >= MaxLines {
				return lines, "line_cap"
			}
			nl := strings.IndexByte(s.text[at:], '\n')
			end := len(s.text)
			if nl >= 0 {
				end = at + nl + 1
			} else if !s.endsLine {
				break
			}
			start := s.start + at
			absoluteEnd := s.start + end
			refs := map[string]bool{}
			for refIndex < len(s.fragments) && s.fragments[refIndex].Start < absoluteEnd {
				active = append(active, s.fragments[refIndex])
				refIndex++
			}
			retained := active[:0]
			for _, f := range active {
				if start < f.End {
					refs[f.ID] = true
					retained = append(retained, f)
				}
			}
			active = retained
			ids := make([]string, 0, len(refs))
			for id := range refs {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			if len(ids) > MaxLineRefs {
				reason = "line_reference_cap"
			} else {
				lines = append(lines, line{start, absoluteEnd, s.text[at:end], ids})
			}
			at = end
		}
	}
	return lines, reason
}
