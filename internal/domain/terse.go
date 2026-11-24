package domain

import "slices"

// Terse answer modes (terse.mode).
const (
	TerseAuto = "auto" // mechanical steps only
	TerseAll  = "all"  // every agent step
	TerseOff  = "off"  // never
)

// TerseDef decides which steps ask the agent for short answers.
type TerseDef struct {
	Mode  string   `json:"mode"`            // auto, all, off
	Steps []string `json:"steps,omitempty"` // step names that are always terse (unless off)
}

// ValidTerse reports whether m is a known terse mode.
func ValidTerse(m string) bool { return m == "" || m == TerseAuto || m == TerseAll || m == TerseOff }

// MechanicalClasses are the model classes of steps that follow a recipe.
var MechanicalClasses = []string{"cheap", "simple"}

// Mechanical reports whether a step is cheap, mechanical work: its model
// class is cheap or simple, or it is pinned to the first (cheapest) model of
// a provider's ladder, like the critic's haiku.
func Mechanical(st StepDef, class string, ladders map[string][]string) bool {
	if st.Type != "" && st.Type != StepAgent && st.Type != StepApproval {
		return false
	}
	if st.Model != "" {
		for _, l := range ladders {
			if len(l) > 0 && l[0] == st.Model {
				return true
			}
		}
		return false
	}
	return slices.Contains(MechanicalClasses, class)
}

// Applies reports whether a step gets the terse-answer instructions.
func (t TerseDef) Applies(st StepDef, class string, ladders map[string][]string) bool {
	switch t.Mode {
	case TerseOff:
		return false
	case TerseAll:
		return true
	}
	return slices.Contains(t.Steps, st.Name) || Mechanical(st, class, ladders)
}
