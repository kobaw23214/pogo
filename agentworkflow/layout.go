package agentworkflow

// Layout is private presentation state, deliberately outside Definition and
// its revision/hash. Moving a node must never change an executable version.
import (
	"fmt"
	"math"
)

const LayoutInputNode = "__input"
const LayoutOutputNode = "__output"

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Viewport struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Zoom float64 `json:"zoom"`
}

type Layout struct {
	WorkflowID         string              `json:"workflow_id"`
	LayoutRevision     int64               `json:"layout_revision"`
	DefinitionRevision int                 `json:"definition_revision"`
	Positions          map[string]Position `json:"positions"`
	Viewport           *Viewport           `json:"viewport,omitempty"`
}

type LayoutSave struct {
	RequestID                  string              `json:"request_id"`
	ExpectedLayoutRevision     int64               `json:"expected_layout_revision"`
	ExpectedDefinitionRevision int                 `json:"expected_definition_revision"`
	Positions                  map[string]Position `json:"positions"`
	Viewport                   *Viewport           `json:"viewport,omitempty"`
}

func validCoordinate(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= 1_000_000
}

// ValidateLayout checks the bounded presentation shape. Membership is checked
// when saving; an old layout may retain a subsequently removed step's position.
func ValidateLayout(positions map[string]Position, viewport *Viewport) error {
	if positions == nil || len(positions) > MaxSteps+2 {
		return fmt.Errorf("%w: layout needs a bounded positions object", ErrInvalid)
	}
	for id, p := range positions {
		if id != LayoutInputNode && id != LayoutOutputNode && !ValidID(id) {
			return fmt.Errorf("%w: invalid layout node ID", ErrInvalid)
		}
		if !validCoordinate(p.X) || !validCoordinate(p.Y) {
			return fmt.Errorf("%w: invalid node coordinates", ErrInvalid)
		}
	}
	if viewport != nil && (!validCoordinate(viewport.X) || !validCoordinate(viewport.Y) || math.IsNaN(viewport.Zoom) || math.IsInf(viewport.Zoom, 0) || viewport.Zoom < 0.1 || viewport.Zoom > 4) {
		return fmt.Errorf("%w: invalid canvas viewport", ErrInvalid)
	}
	return nil
}

func LayoutNodes(d Definition) map[string]bool {
	nodes := map[string]bool{LayoutInputNode: true, LayoutOutputNode: true}
	for _, step := range d.Steps {
		nodes[step.ID] = true
	}
	return nodes
}

// ProjectLayout removes stale coordinates from the response without writing
// anything or advancing either revision. Historical Runs use their own graph.
func ProjectLayout(layout Layout, d Definition) Layout {
	nodes := LayoutNodes(d)
	positions := make(map[string]Position, len(layout.Positions))
	for id, p := range layout.Positions {
		if nodes[id] {
			positions[id] = p
		}
	}
	layout.WorkflowID = d.ID
	layout.DefinitionRevision = d.Revision
	layout.Positions = positions
	return layout
}
