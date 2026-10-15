package gitz3

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

func (l *Lab) Run(sc *Scenario, reps []RepDef) *Result {
	return &Result{Scenario: sc, Rows: l.RunStates(sc.States, reps)}
}

func (r *Result) row(state, rep string) *Row {
	for i := range r.Rows {
		if r.Rows[i].State == state && r.Rows[i].Rep == rep {
			return &r.Rows[i]
		}
	}
	return nil
}

func human(n int64) string {
	switch {
	case n >= 10<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 10<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d", n)
}

// WriteMD writes a markdown table: one row per representation, per state the
// incremental production-pack-rule cost (table-layout cost without pack compression in parentheses).
// Non-exact rows are flagged, oracle rows are italicised.
func (r *Result) WriteMD(w io.Writer, packed bool) {
	sc := r.Scenario
	fmt.Fprintf(w, "\n**%s** — %s\n\n", sc.Name, sc.Desc)
	fmt.Fprint(w, "| representation |")
	for _, s := range sc.States {
		fmt.Fprintf(w, " %s (%s) |", s.Label, human(s.Bytes()))
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
			if rw := r.row(s.Label, rp); rw != nil && rw.Oracle {
				name = "*" + rp + " (oracle)*"
			}
			break
		}
		fmt.Fprintf(w, "| %s |", name)
		for _, s := range sc.States {
			rw := r.row(s.Label, rp)
			if rw == nil {
				fmt.Fprint(w, " - |")
				continue
			}
			flag := ""
			if !rw.Oracle && !rw.Exact {
				flag = " ✗"
			}
			if packed {
				fmt.Fprintf(w, " %s (%s)%s |", human(rw.CostPacked), human(rw.Cost), flag)
			} else {
				fmt.Fprintf(w, " %s%s |", human(rw.Cost), flag)
			}
		}
		fmt.Fprintln(w)
	}
}

// WriteChurn prints a Churn as a one-line-per-class table.
func WriteChurn(w io.Writer, label string, c Churn) {
	pct := func(v int64) string {
		if c.BytesB == 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(v)/float64(c.BytesB))
	}
	fmt.Fprintf(w, "| %s | %d/%d | %s | %s | %s | %s | %s | %s | %d: %d / %d |\n", label, c.Shared, c.EntriesB, pct(c.New), pct(c.Same), pct(c.OFSShift), pct(c.ZlibOnly), pct(c.DeltaChange), pct(c.AdjBroken), c.DeltaBoth, c.DeltaSameBase, c.DeltaSameProg)
}

func ChurnHeader(w io.Writer) {
	fmt.Fprintln(w, "| pair | shared objs / B entries | new objs | identical entry | OFS-shift only | zlib bytes only | delta base/program | adjacency broken (of identical) | delta entries in both: same base / same program |")
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|---:|---:|---:|")
}

func (s PackShape) String() string {
	var ks []string
	for _, d := range SortedKeys(s.DepthHist) {
		ks = append(ks, fmt.Sprintf("%d:%d", d, s.DepthHist[d]))
	}
	sort.Strings(ks)
	return fmt.Sprintf("entries=%d full=%d ofs=%d ref=%d maxdepth=%d entryB=%d zlibB=%d inflatedB=%d resolvedB=%d hdrB=%d small=%d", s.Entries, s.Full, s.OFS, s.REF, s.MaxDepth, s.EntryBytes, s.ZlibBytes, s.InflatedBytes, s.ResolvedBytes, s.HeaderBytes, s.SmallEntries)
}

var _ = strings.TrimSpace

// WriteDetail prints the cost composition of the named representations in every state.
func (r *Result) WriteDetail(w io.Writer, reps ...string) {
	fmt.Fprintln(w, "\n| state | representation | new comp bytes | recipe (table) | plan meta | index | new comps | refs | reused | new by class |")
	fmt.Fprintln(w, "|---|---|---:|---:|---:|---:|---:|---:|---:|---|")
	for _, s := range r.Scenario.States {
		for _, rp := range reps {
			rw := r.row(s.Label, rp)
			if rw == nil {
				continue
			}
			var cl []string
			for k, v := range rw.Detail {
				if strings.HasPrefix(k, "new_") {
					cl = append(cl, fmt.Sprintf("%s=%s", strings.TrimPrefix(k, "new_"), human(v)))
				} else {
					cl = append(cl, fmt.Sprintf("%s=%d", k, v))
				}
			}
			sort.Strings(cl)
			fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %d | %d | %d | %s |\n", s.Label, rp, human(rw.NewBytes), human(rw.Recipe), human(rw.Meta), human(rw.Index), rw.NewComps, rw.Comps, rw.Reused, strings.Join(cl, " "))
		}
	}
}
