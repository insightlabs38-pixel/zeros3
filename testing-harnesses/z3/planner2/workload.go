package planner2

import (
	"fmt"
	"sort"
)

// ArrivalsOnly builds the minimal world of Z3-36: one lineage, arrivals in order.
func ArrivalsOnly(l Lineage) *World {
	w := &World{Name: l.Name(), Lineages: []Lineage{l}}
	for t := 0; t < l.Len(); t++ {
		w.Events = append(w.Events, Event{Seq: t, Day: t, Kind: Arrive, Obj: t, Lineage: 0, Stage: t, Key: l.KeyHint(), Size: l.Logical(t)})
	}
	return w
}

// Spec describes a SIMULATED chronology over real lineages: arrival order within a
// lineage is the captured one; start days, spacing, retention, reads, tiering and
// maintenance windows are labelled synthetic.
type Spec struct {
	Name             string
	Seed             int64
	StartWindowDays  int
	StepMin, StepMax int
	Retention        string // all | last3 | last3snap | ttl14 | oneoff14
	Reads            string // none | hotlatest | archive | mixed
	KeyMode          string // good | none | misleading
	TickEvery        int
	Window           Budget
	GCEvery          int
	Horizon          int  // days simulated after the last arrival (reads/ticks continue)
	Reverse          bool // adversarial: stages arrive in reverse order (series only)
}

func DefaultSpec(name string) Spec {
	return Spec{Name: name, Seed: 1, StartWindowDays: 60, StepMin: 2, StepMax: 9, Retention: "all", Reads: "none", KeyMode: "good",
		TickEvery: 7, Window: Budget{CPUMS: 900_000, IOBytes: 8 << 30}, GCEvery: 2, Horizon: 30}
}

type pending struct {
	day, order int
	ev         Event
	li, st     int
}

// Build assembles the chronological event stream deterministically from the spec.
func Build(sp Spec, lins []Lineage) *World {
	w := &World{Name: sp.Name, Lineages: lins}
	var evs []pending
	arrDay := map[[2]int]int{}
	maxDay := 0
	keys := []string{"data/a", "data/b", "data/c", "data/d"}
	for li, l := range lins {
		start := int(hash01(sp.Seed, uint64(li), 1) * float64(sp.StartWindowDays+1))
		day := start
		for t := 0; t < l.Len(); t++ {
			if t > 0 {
				span := sp.StepMax - sp.StepMin + 1
				if span < 1 {
					span = 1
				}
				day += sp.StepMin + int(hash01(sp.Seed, uint64(li), uint64(t), 2)*float64(span))
			}
			arrDay[[2]int{li, t}] = day
			key := ""
			switch sp.KeyMode {
			case "good":
				key = l.Name()
			case "misleading":
				key = keys[int(hash01(sp.Seed, uint64(li), 9)*float64(len(keys)))]
				if hash01(sp.Seed, uint64(li), uint64(t), 10) < 0.3 {
					key = keys[int(hash01(sp.Seed, uint64(li), uint64(t), 11)*float64(len(keys)))]
				}
			}
			evs = append(evs, pending{day: day, order: 0, li: li, st: t, ev: Event{Kind: Arrive, Key: key, Size: l.Logical(t), TierHint: "hot"}})
			if day > maxDay {
				maxDay = day
			}
		}
	}
	// deletes (retention) — SIMULATED
	for li, l := range lins {
		n := l.Len()
		for t := 0; t < n; t++ {
			d := -1
			switch sp.Retention {
			case "last3":
				if t+3 < n {
					d = arrDay[[2]int{li, t + 3}]
				}
			case "last3snap":
				if t%4 != 0 && t+3 < n {
					d = arrDay[[2]int{li, t + 3}]
				}
			case "ttl14":
				d = arrDay[[2]int{li, t}] + 14
			case "oneoff14":
				if n == 1 {
					d = arrDay[[2]int{li, t}] + 14
				}
			}
			if d >= 0 {
				if d == arrDay[[2]int{li, t}] {
					d++
				}
				evs = append(evs, pending{day: d, order: 1, li: li, st: t, ev: Event{Kind: Delete}})
				if d > maxDay {
					maxDay = d
				}
			}
		}
	}
	end := maxDay + sp.Horizon
	// tier transitions and reads — SIMULATED
	hotClass := func(li int) bool {
		switch sp.Reads {
		case "hotlatest":
			return true
		case "mixed":
			return hash01(sp.Seed, uint64(li), 21) < 0.3
		}
		return false
	}
	for li, l := range lins {
		n := l.Len()
		for t := 0; t < n; t++ {
			a := arrDay[[2]int{li, t}]
			evs = append(evs, pending{day: a + 7, order: 3, li: li, st: t, ev: Event{Kind: Tier, TierHint: "warm"}})
			evs = append(evs, pending{day: a + 30, order: 3, li: li, st: t, ev: Event{Kind: Tier, TierHint: "cold"}})
			if sp.Reads == "none" {
				continue
			}
			for d := a; d <= end; d++ {
				age := d - a
				latest := t == n-1 || arrDay[[2]int{li, t + 1}] > d
				rate := 0.005
				if hotClass(li) {
					switch {
					case latest && age < 30:
						rate = 2.0
					case latest:
						rate = 0.3
					case age < 30:
						rate = 0.3
					default:
						rate = 0.02
					}
				}
				k := int(rate)
				if hash01(sp.Seed, uint64(li), uint64(t), uint64(d), 31) < rate-float64(k) {
					k++
				}
				for j := 0; j < k; j++ {
					v := hash01(sp.Seed, uint64(li), uint64(t), uint64(d), uint64(40+j))
					var rg int64
					switch {
					case v < 0.10:
						rg = 0
					case v < 0.60:
						rg = 4 << 10
					case v < 0.90:
						rg = 64 << 10
					default:
						rg = 1 << 20
					}
					evs = append(evs, pending{day: d, order: 2, li: li, st: t, ev: Event{Kind: Read, Range: rg}})
				}
			}
		}
	}
	// maintenance ticks
	tick := 0
	for d := sp.TickEvery; d <= end; d += sp.TickEvery {
		tick++
		evs = append(evs, pending{day: d, order: 4, li: -1, ev: Event{Kind: Tick, Window: true, Budget: sp.Window, GC: sp.GCEvery > 0 && tick%sp.GCEvery == 0}})
	}
	sort.SliceStable(evs, func(i, j int) bool {
		a, b := evs[i], evs[j]
		if a.day != b.day {
			return a.day < b.day
		}
		if a.order != b.order {
			return a.order < b.order
		}
		if a.li != b.li {
			return a.li < b.li
		}
		return a.st < b.st
	})
	obj := map[[2]int]int{}
	nextObj := 0
	for seq, p := range evs {
		e := p.ev
		e.Seq, e.Day = seq, p.day
		switch e.Kind {
		case Arrive:
			e.Obj, e.Lineage, e.Stage = nextObj, p.li, p.st
			obj[[2]int{p.li, p.st}] = nextObj
			nextObj++
		case Delete, Read, Tier:
			id, ok := obj[[2]int{p.li, p.st}]
			if !ok {
				continue // would precede its arrival (cannot happen for ordered events)
			}
			e.Obj, e.Lineage, e.Stage = id, p.li, p.st
		}
		w.Events = append(w.Events, e)
	}
	// re-number sequentially after the filter
	for i := range w.Events {
		w.Events[i].Seq = i
	}
	w.Simulated = []string{"arrival-days", "retention:" + sp.Retention, "reads:" + sp.Reads, "tiers", "ticks", "keys:" + sp.KeyMode}
	return w
}

// Describe summarises a world for logs.
func (w *World) Describe() string {
	var a, d, r, t, k int
	for _, e := range w.Events {
		switch e.Kind {
		case Arrive:
			a++
		case Delete:
			d++
		case Read:
			r++
		case Tier:
			t++
		case Tick:
			k++
		}
	}
	return fmt.Sprintf("%s: %d lineages, arrive=%d delete=%d read=%d tier=%d tick=%d", w.Name, len(w.Lineages), a, d, r, t, k)
}
