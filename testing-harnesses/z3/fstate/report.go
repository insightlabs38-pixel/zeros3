package fstate

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Scenario is a labelled sequence of states measured under a set of representations.
type Scenario struct {
	Name, Desc string
	States     []*State
}

// Result is the rows of a scenario.
type Result struct {
	Scenario *Scenario
	Rows     []Row
}

func (r *Runner) Run(sc *Scenario, reps []RepDef) *Result {
	return &Result{Scenario: sc, Rows: r.RunStates(sc.States, reps)}
}

func (r *Result) Row(state, rep string) *Row {
	for i := range r.Rows {
		if r.Rows[i].State == state && r.Rows[i].Rep == rep {
			return &r.Rows[i]
		}
	}
	return nil
}

// Human formats a byte count compactly (bytes, K, M).
func Human(n int64) string {
	switch {
	case n >= 10<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 10<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d", n)
}

// WriteMD writes a markdown table: one row per representation, per state the
// incremental production-pack-rule cost (table-layout cost without pack
// compression in parentheses). Non-exact rows are flagged, oracle rows italicised.
func (r *Result) WriteMD(w io.Writer, packed bool) {
	sc := r.Scenario
	fmt.Fprintf(w, "\n**%s** — %s\n\n", sc.Name, sc.Desc)
	fmt.Fprint(w, "| representation |")
	for _, s := range sc.States {
		fmt.Fprintf(w, " %s (%s) |", s.Label, Human(s.Bytes()))
	}
	fmt.Fprint(w, "\n|---|")
	for range sc.States {
		fmt.Fprint(w, "---:|")
	}
	fmt.Fprintln(w)
	var reps []string
	seen := map[string]bool{}
	for _, rw := range r.Rows {
		if !seen[rw.Rep] {
			seen[rw.Rep] = true
			reps = append(reps, rw.Rep)
		}
	}
	for _, rp := range reps {
		name := rp
		for _, s := range sc.States {
			if rw := r.Row(s.Label, rp); rw != nil && rw.Oracle {
				name = "*" + rp + " (oracle)*"
			}
			break
		}
		fmt.Fprintf(w, "| %s |", name)
		for _, s := range sc.States {
			rw := r.Row(s.Label, rp)
			if rw == nil {
				fmt.Fprint(w, " - |")
				continue
			}
			flag := ""
			if !rw.Oracle && !rw.Exact {
				flag = " ✗"
			}
			if packed {
				fmt.Fprintf(w, " %s (%s)%s |", Human(rw.CostPacked), Human(rw.Cost), flag)
			} else {
				fmt.Fprintf(w, " %s%s |", Human(rw.Cost), flag)
			}
		}
		fmt.Fprintln(w)
	}
}

// WriteDetail prints the cost composition of the named representations in every state.
func (r *Result) WriteDetail(w io.Writer, reps ...string) {
	fmt.Fprintln(w, "\n| state | representation | new comp bytes | recipe (table) | plan meta | index | new comps | refs | reused | new by class |")
	fmt.Fprintln(w, "|---|---|---:|---:|---:|---:|---:|---:|---:|---|")
	for _, s := range r.Scenario.States {
		for _, rp := range reps {
			rw := r.Row(s.Label, rp)
			if rw == nil {
				continue
			}
			var cl []string
			for k, v := range rw.Detail {
				if strings.HasPrefix(k, "new_") {
					cl = append(cl, fmt.Sprintf("%s=%s", strings.TrimPrefix(k, "new_"), Human(v)))
				} else {
					cl = append(cl, fmt.Sprintf("%s=%d", k, v))
				}
			}
			sort.Strings(cl)
			fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %d | %d | %d | %s |\n", s.Label, rp, Human(rw.NewBytes), Human(rw.Recipe), Human(rw.Meta), Human(rw.Index), rw.NewComps, rw.Comps, rw.Reused, strings.Join(cl, " "))
		}
	}
}
