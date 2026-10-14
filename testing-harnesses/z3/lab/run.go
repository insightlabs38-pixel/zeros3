package lab

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"runtime"
	"time"

	"zeros3-z3/corpus"
)

const ReportSchema = "z3-lab/1" // experimental; not a compatibility surface

// RunConfig controls a laboratory run.
type RunConfig struct {
	Dir        string
	RefBytes   int64    // recipe bytes charged per component reference
	Pack       bool     // estimate production pack compression
	Families   []string // globs; empty = all
	Reps       []string // names; empty = all
	RangeReads int      // sampled range reads per revision (Concat plans)
	Opts       Options
	Progress   io.Writer
}

// DefaultRefBytes approximates one manifest chunkRef in the JSON manifest:
// {"sha256":"<64 hex>","length":N}, with separators.
const DefaultRefBytes = 96

func DefaultRunConfig(dir string) RunConfig {
	return RunConfig{Dir: dir, RefBytes: DefaultRefBytes, Pack: true, RangeReads: 3, Opts: DefaultOptions()}
}

// Row is one (family, revision, representation) measurement. Nullable fields
// are N/A rather than invented.
type Row struct {
	Family   string `json:"family"`
	Format   string `json:"format"`
	Revision string `json:"revision"`
	Index    int    `json:"index"`
	Rep      string `json:"representation"`
	Status   string `json:"status"` // ok | invalid | error
	Error    string `json:"error,omitempty"`
	Oracle   bool   `json:"oracle,omitempty"`
	CostNA   bool   `json:"cost_na,omitempty"` // experimental representation: no physical cost defined

	LogicalSize int64 `json:"logical_size"`
	Account
	IncrementalPacked *int64  `json:"incremental_packed_bytes"`
	CumulativePacked  *int64  `json:"cumulative_packed_bytes"`
	ReusePct          float64 `json:"reuse_pct"`

	VsRawBytes         int64          `json:"vs_raw_bytes"`
	VsCDCBytes         *int64         `json:"vs_cdc_bytes"`
	VsCDCPackedBytes   *int64         `json:"vs_cdc_packed_bytes"`
	BuildMs            float64        `json:"build_time_ms"`
	ReconstructMs      *float64       `json:"reconstruct_time_ms"`
	ReconstructMBps    *float64       `json:"reconstruct_mb_per_s"`
	AllocBytes         uint64         `json:"build_alloc_bytes"`
	RoundtripExact     string         `json:"roundtrip_exact"` // yes | NO | N/A
	RangeReads         int            `json:"range_reads"`
	RangeAmplification *float64       `json:"range_read_amplification"`
	Detail             map[string]any `json:"detail,omitempty"`
}

type Report struct {
	Schema    string        `json:"schema"`
	Generated string        `json:"generated_by"`
	Corpus    corpus.Config `json:"corpus"`
	GoVersion string        `json:"go_version"`
	RefBytes  int64         `json:"ref_bytes"`
	Pack      bool          `json:"pack_estimate"`
	Rows      []Row         `json:"rows"`
}

func i64p(v int64) *int64     { return &v }
func f64p(v float64) *float64 { return &v }

type hashCounter struct {
	h hash.Hash
	n int64
}

func (w *hashCounter) Write(p []byte) (int, error) { w.n += int64(len(p)); return w.h.Write(p) }

// Run measures every selected representation over every selected family.
func Run(cfg RunConfig) (*Report, error) {
	m, err := corpus.ReadManifest(cfg.Dir)
	if err != nil {
		return nil, err
	}
	if cfg.RefBytes < 0 {
		return nil, errors.New("lab: negative ref bytes")
	}
	if cfg.Opts.InlineMax == 0 {
		cfg.Opts = DefaultOptions()
	}
	reps := Reps()
	if len(cfg.Reps) > 0 {
		var sel []Rep
		for _, name := range cfg.Reps {
			found := false
			for _, r := range reps {
				if r.Name == name {
					sel, found = append(sel, r), true
				}
			}
			if !found {
				return nil, fmt.Errorf("lab: unknown representation %q", name)
			}
		}
		reps = sel
	}
	rep := &Report{Schema: ReportSchema, Generated: "z3lab", Corpus: m.Config, GoVersion: runtime.Version(), RefBytes: cfg.RefBytes, Pack: cfg.Pack}
	names, by := m.Group()
	for _, fam := range names {
		if !matchAny(cfg.Families, fam) {
			continue
		}
		rows, err := runFamily(cfg, reps, by[fam])
		if err != nil {
			return nil, fmt.Errorf("family %s: %w", fam, err)
		}
		rep.Rows = append(rep.Rows, rows...)
		if cfg.Progress != nil {
			fmt.Fprintf(cfg.Progress, "done %s\n", fam)
		}
	}
	return rep, nil
}

func matchAny(pats []string, name string) bool {
	if len(pats) == 0 {
		return true
	}
	for _, p := range pats {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

func runFamily(cfg RunConfig, reps []Rep, members []corpus.Member) ([]Row, error) {
	accts := make([]*Acct, len(reps))
	for i := range accts {
		accts[i] = NewAcct(cfg.RefBytes, cfg.Pack)
	}
	var closers []io.Closer
	defer func() {
		for _, c := range closers {
			c.Close()
		}
	}()
	var rows []Row
	var prev *Input
	for _, mem := range members {
		p, err := corpus.MemberPath(cfg.Dir, mem)
		if err != nil {
			return nil, err
		}
		src, cl, err := OpenSource(p)
		if err != nil {
			return nil, err
		}
		closers = append(closers, cl)
		if src.Size != mem.Size {
			return nil, fmt.Errorf("%s: size %d, manifest %d", mem.Path, src.Size, mem.Size)
		}
		in := &Input{Family: mem.Family, Format: mem.Format, Revision: mem.Revision, Src: src, Prev: prev, Opts: cfg.Opts}
		if prev != nil {
			prev.Prev = nil // keep only one level of history
			prev.parsed = nil
		}
		h := sha256.New()
		r, _ := src.Reader(0, src.Size)
		if _, err := io.Copy(h, r); err != nil {
			return nil, err
		}
		want := hex.EncodeToString(h.Sum(nil))
		if want != mem.SHA256 {
			return nil, fmt.Errorf("%s: content does not match manifest sha256", mem.Path)
		}
		first := len(rows)
		for ri, rp := range reps {
			row, ok := measure(cfg, rp, accts[ri], in, mem, want)
			if ok {
				rows = append(rows, row)
			}
		}
		fillComparisons(rows[first:])
		prev = in
	}
	return rows, nil
}

// measure builds, accounts and verifies one representation of one revision.
func measure(cfg RunConfig, rp Rep, ac *Acct, in *Input, mem corpus.Member, wantSHA string) (Row, bool) {
	row := Row{Family: mem.Family, Format: mem.Format, Revision: mem.Revision, Index: mem.Index, Rep: rp.Name,
		LogicalSize: mem.Size, Status: "ok", RoundtripExact: "N/A"}
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	t0 := time.Now()
	plan, err := rp.Analyze(in)
	row.BuildMs = ms(time.Since(t0))
	runtime.ReadMemStats(&m1)
	row.AllocBytes = m1.TotalAlloc - m0.TotalAlloc
	if errors.Is(err, ErrNotApplicable) {
		return row, false
	}
	if err != nil {
		// Transform failure is an optimisation failure, not object invalidity.
		row.Status, row.Error = "error", err.Error()
		return row, true
	}
	row.Oracle = plan.Oracle
	row.Detail = plan.Detail
	var acct Account
	if plan.NoCost {
		row.CostNA = true
	} else {
		acct, err = ac.Add(plan)
		if err != nil {
			row.Status, row.Error = "error", err.Error()
			return row, true
		}
		row.Account = acct
		if acct.PackedKnown {
			row.IncrementalPacked, row.CumulativePacked = i64p(acct.IncrementalPack), i64p(acct.CumulativePack)
		}
		total := acct.ReuseBytes + acct.NewBytes
		if total > 0 {
			row.ReusePct = 100 * float64(acct.ReuseBytes) / float64(total)
		}
	}
	if plan.Rebuild != nil && !plan.Oracle {
		hc := &hashCounter{h: sha256.New()}
		t1 := time.Now()
		rerr := plan.Rebuild(hc, ac.Get)
		dt := time.Since(t1)
		got := hex.EncodeToString(hc.h.Sum(nil))
		if rerr != nil || hc.n != mem.Size || got != wantSHA {
			row.RoundtripExact, row.Status = "NO", "invalid"
			if rerr != nil {
				row.Error = rerr.Error()
			} else {
				row.Error = fmt.Sprintf("reconstructed %d bytes sha256 %.12s, want %d bytes %.12s", hc.n, got, mem.Size, wantSHA)
			}
		} else {
			row.RoundtripExact = "yes"
		}
		row.ReconstructMs = f64p(ms(dt))
		if dt > 0 {
			row.ReconstructMBps = f64p(float64(mem.Size) / (1 << 20) / dt.Seconds())
		}
		if row.Status == "ok" && plan.Concat && cfg.RangeReads > 0 {
			measureRanges(&row, plan, ac, in, mem, cfg.RangeReads)
		}
	}
	return row, true
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// measureRanges reads deterministic ranges and checks them against the source.
func measureRanges(row *Row, plan *Plan, ac *Acct, in *Input, mem corpus.Member, count int) {
	sizes := []int64{4 << 10, 64 << 10, 1 << 20}
	var fetched, asked int64
	for k := 0; k < count; k++ {
		n := min(sizes[k%len(sizes)], mem.Size/2)
		if n <= 0 {
			continue
		}
		hsh := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%d", mem.Family, mem.Revision, k)))
		off := int64(uint64(hsh[0])<<24|uint64(hsh[1])<<16|uint64(hsh[2])<<8|uint64(hsh[3])) % (mem.Size - n + 1)
		got, f, err := ReadRange(plan, ac.Get, off, n)
		want, werr := in.Src.Span(off, n)
		if err != nil || werr != nil || string(got) != string(want) {
			row.Status, row.Error = "invalid", fmt.Sprintf("range read [%d,+%d) mismatch (%v)", off, n, err)
			return
		}
		fetched, asked = fetched+f, asked+n
		row.RangeReads++
	}
	if asked > 0 {
		row.RangeAmplification = f64p(float64(fetched) / float64(asked))
	}
}

// fillComparisons computes per-revision savings against raw and CDC.
func fillComparisons(rows []Row) {
	var raw, cdc *Row
	for i := range rows {
		switch rows[i].Rep {
		case "raw":
			raw = &rows[i]
		case "cdc":
			cdc = &rows[i]
		}
	}
	for i := range rows {
		r := &rows[i]
		if r.Status == "error" || r.CostNA {
			continue
		}
		if raw != nil && raw.Status == "ok" {
			r.VsRawBytes = raw.Incremental - r.Incremental
		}
		if cdc != nil && cdc.Status == "ok" {
			r.VsCDCBytes = i64p(cdc.Incremental - r.Incremental)
			if r.IncrementalPacked != nil && cdc.IncrementalPacked != nil {
				r.VsCDCPackedBytes = i64p(*cdc.IncrementalPacked - *r.IncrementalPacked)
			}
		}
	}
}
