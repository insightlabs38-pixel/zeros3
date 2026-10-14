package lab

import (
	"bytes"
	"errors"
	"time"
)

// NamedBytes is one in-memory revision of a synthetic family.
type NamedBytes struct {
	Label string
	Data  []byte
}

// FRow is one (revision, representation) measurement of RunBytesFamily.
type FRow struct {
	Family, Rep, Label string
	Rev                int
	Size               int64
	Status             string // ok | n/a | error | invalid
	Err                string
	Exact              bool
	Acct               Account
	CostPacked         int64 // with production pack compression of the new components (RunBytesFamilyPacked)
	Cost               int64 // NewBytes + table-layout recipe + plan meta + index bytes
	Detail             map[string]any
	BuildMs            float64
}

// TableCost is the incremental physical cost under the per-object digest table
// recipe layout (recipe.go): new component bytes + the serialised piece table
// + the plan's own meta and index bytes. Every representation is charged the
// same way, so it is the headline comparison for the Z3-14/15 experiments.
func TableCost(a Account) int64 {
	return a.NewBytes + a.Layouts["table"] + a.PlanMetaBytes + a.IndexBytes
}

// RunBytesFamily runs each representation over the chain of revisions, with
// one fresh component store per representation, and verifies exact
// reconstruction of every accepted revision.
func RunBytesFamily(family, format string, revs []NamedBytes, reps []Rep) []FRow {
	return RunBytesFamilyPacked(family, format, revs, reps, false)
}

// RunBytesFamilyPacked is RunBytesFamily with the production pack-compression
// estimate (DEFLATE, kept when it saves 1/16) applied to new components; the
// result is in FRow.CostPacked.
func RunBytesFamilyPacked(family, format string, revs []NamedBytes, reps []Rep, pack bool) []FRow {
	var rows []FRow
	for _, rp := range reps {
		ac := NewAcct(35, pack)
		var prev *Input
		for i, rv := range revs {
			in := &Input{Family: family, Format: format, Revision: rv.Label, Src: BytesSource(family+"/"+rv.Label, rv.Data), Prev: prev, Opts: DefaultOptions()}
			prev = in
			row := FRow{Family: family, Rep: rp.Name, Label: rv.Label, Rev: i, Size: int64(len(rv.Data)), Status: "ok"}
			t0 := time.Now()
			plan, err := rp.Analyze(in)
			row.BuildMs = ms(time.Since(t0))
			if errors.Is(err, ErrNotApplicable) {
				row.Status = "n/a"
				rows = append(rows, row)
				continue
			}
			if err != nil {
				row.Status, row.Err = "error", err.Error()
				rows = append(rows, row)
				continue
			}
			row.Detail = plan.Detail
			if row.Acct, err = ac.Add(plan); err != nil {
				row.Status, row.Err = "error", err.Error()
				rows = append(rows, row)
				continue
			}
			row.Cost = TableCost(row.Acct)
			if row.Acct.PackedKnown {
				row.CostPacked = row.Acct.PackedNew + row.Acct.Layouts["table"] + row.Acct.PlanMetaBytes + row.Acct.IndexBytes
			}
			var out bytes.Buffer
			if rerr := plan.Rebuild(&out, ac.Get); rerr != nil || !bytes.Equal(out.Bytes(), rv.Data) {
				row.Status = "invalid"
				if rerr != nil {
					row.Err = rerr.Error()
				}
			} else {
				row.Exact = true
			}
			rows = append(rows, row)
		}
	}
	return rows
}
