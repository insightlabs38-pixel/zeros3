// Package fmtcap is the executable model of the proposed composite store-format
// semantics for features that must not be mixed up by numeric comparison:
// P1C small-object segments (level 11 on main) and representation authority
// (level 10 in the unmerged B2 draft). It models gates only; it is not wired
// into zeros3.go and activates nothing.
package fmtcap

// Feature bits a store may REQUIRE (every reader must implement each).
const (
	Segments  = 1 << iota // P1C segment files hold manifest/chunk bytes
	Authority             // body records, not manifest chunk lists, root liveness
)

// Level is the FORMAT.json store_format_version published for a feature set.
// Levels are labels, not an order: 10 and 11 are unrelated, 12 requires both.
func Level(f int) int {
	switch f {
	case 0:
		return 9 // the representations floor: no feature below needed
	case Segments:
		return 11
	case Authority:
		return 10
	case Segments | Authority:
		return 12
	}
	return -1
}

// Features returns the feature set required by a level (ok=false: unknown label).
func Features(level int) (f int, ok bool) {
	switch {
	case level >= 1 && level <= 9:
		return 0, true
	case level == 11:
		return Segments, true
	case level == 10:
		return Authority, true
	case level == 12:
		return Segments | Authority, true
	}
	return 0, false
}

// Binary models a build by the features it implements and the labels it accepts.
type Binary struct {
	Name     string
	Features int
}

// Accepts is the proposed gate: the label must be known AND every feature it
// requires must be implemented. (A numeric `level <= max` gate is NOT this.)
func (b Binary) Accepts(level int) bool {
	f, ok := Features(level)
	return ok && f&^b.Features == 0
}

// MaxGate is the incorrect numeric rule, kept to demonstrate why it is unsafe.
func MaxGate(max int) func(int) bool {
	return func(l int) bool { return l >= 1 && l <= max }
}

// RaiseTo returns the level a store at `from` must be published at before the
// first record of feature x exists; ok=false if from is not a valid label.
func RaiseTo(from, x int) (int, bool) {
	f, ok := Features(from)
	if !ok {
		return 0, false
	}
	return Level(f | x), true
}

// CanRetire reports whether feature x may be dropped from the required set: only
// when no record of that feature remains (authority: zero bodies, CDC chunks
// physically materialized and fsynced). Segments: no segment file holds live data.
func CanRetire(f, x int, recordsRemaining int) (newLevel int, ok bool) {
	if f&x == 0 || recordsRemaining > 0 {
		return Level(f), false
	}
	return Level(f &^ x), true
}
