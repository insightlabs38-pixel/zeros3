// Package planner2 is the Z4-B7 research planner: a causal, replayable decision
// library over exact representation candidates. It is laboratory code only: no
// production planner primitive, background worker or persistent format.
package planner2

import (
	"errors"
	"fmt"
)

// CompID names one physical component (content-addressed object, pack member).
type CompID string

// CompRef is one closure member: the bytes charged when the component is new.
type CompRef struct {
	ID    CompID
	Bytes int64
}

// Cand is one immutable exact candidate for one object, produced by analysis.
// Comps is the complete reconstruction closure (dependencies included).
type Cand struct {
	Scheme, Form, Family string
	Comps                []CompRef
	Base                 int // object id of the direct base, -1 if none
	Depth                int // dependency depth of this form (0 = standalone)
	Sample               []string
	LogicalBytes         int64
	AnalysisMS           float64 // charged when analysed
	ReadBytes            int64   // source bytes read to encode (whole-object replay in Z3-36)
	DecodeMiBs           float64
	Amp4K, Amp64K, Amp1M float64
	Physical             string // "measured" | "modeled"
	SrcSHA, RootSHA      string // exact identities (Z3-36 frontier) or synthetic ids
	Ready                bool
}

// OwnBytes is the sum of Comps bytes (standalone closure size, ignoring sharing).
func (c Cand) OwnBytes() int64 {
	var n int64
	for _, r := range c.Comps {
		n += r.Bytes
	}
	return n
}

// Validate rejects malformed, negative-cost, duplicate-component or not-ready candidates.
func (c Cand) Validate() error {
	if !c.Ready {
		return errors.New("planner2: candidate not ready (future opportunity)")
	}
	if c.Scheme == "" || len(c.Comps) == 0 {
		return errors.New("planner2: candidate without scheme or closure")
	}
	seen := map[CompID]bool{}
	for _, r := range c.Comps {
		if r.ID == "" || r.Bytes < 0 {
			return fmt.Errorf("planner2: bad component %q bytes %d", r.ID, r.Bytes)
		}
		if seen[r.ID] {
			return fmt.Errorf("planner2: duplicate component %q in closure", r.ID)
		}
		seen[r.ID] = true
	}
	if c.Depth < 0 || c.AnalysisMS < 0 || c.ReadBytes < 0 {
		return errors.New("planner2: negative metadata")
	}
	return nil
}

// Kind of event delivered to a policy, strictly chronological.
type Kind int

const (
	Arrive Kind = iota
	Delete
	Read
	Tier
	Tick
)

func (k Kind) String() string { return [...]string{"arrive", "delete", "read", "tier", "tick"}[k] }

// Event is everything a policy may observe about one moment. Lineage/Stage are
// runner bookkeeping and are never exposed to policies except through Arrival.
type Event struct {
	Seq, Day int
	Kind     Kind
	Obj      int
	Lineage  int // runner-side only
	Stage    int // runner-side only
	Key      string
	Size     int64
	TierHint string // "hot" | "warm" | "cold" | ""
	Range    int64  // Read: requested bytes (0 = whole object)
	Window   bool   // Tick: maintenance window open
	Budget   Budget // Tick: resources available this window
	GC       bool   // Tick: garbage collection runs
}

// Arrival is the policy-visible part of an arrive event.
type Arrival struct {
	Seq, Day, Obj int
	Key           string
	Size          int64
	TierHint      string
}

// Budget is a per-window or per-PUT resource allowance.
type Budget struct {
	CPUMS, IOBytes, TempBytes int64
}

// Action is a policy output. Exactly one of the arrival decisions or maintenance
// actions is meaningful depending on the entry point.
type ActKind int

const (
	ActStore    ActKind = iota // arrival: store chosen candidate (default cdc)
	ActMigrate                 // maintenance: move Obj to candidate (Scheme, Form)
	ActEnqueue                 // schedule deferred analysis of Obj for Scheme
	ActReopt                   // run a group reoptimizer on Obj's lineage
	ActFlatten                 // rebase Obj to a standalone form of its scheme
	ActRevert                  // return Obj to CDC
	ActDropSide                // drop an additive sidecar
)

func (k ActKind) String() string {
	return [...]string{"store", "migrate", "enqueue", "reopt", "flatten", "revert", "dropside"}[k]
}

type Action struct {
	Kind          ActKind
	Obj           int
	Scheme, Form  string
	Sidecar       bool // additive: keep CDC and also store the representation
	Priority      float64
	Reason        string
	ExpectedSaved int64
}

// Decision is the admission result for an arrival.
type Decision struct {
	Scheme, Form string
	Sidecar      bool
	Defer        []Action // deferred analyses to enqueue after the (safe) store
	Reason       string
}

// CDC is the authoritative fallback scheme name.
const CDC = "cdc"

// SchemeInfo is static, pre-measured knowledge about a representation (from
// earlier research); it is available before any analysis of the object.
type SchemeInfo struct {
	Name, Family, Kind     string // Kind: "cdc" | "rep"
	Dep                    string // "none" | "cas" | "chain"
	DepthCap               int
	AnalysisMiBs           float64
	DecodeMiBs             float64
	Amp4K, Amp64K, Amp1M   float64
	Ready                  bool
	AmpSrc, StdSrc, IncSrc string
}
