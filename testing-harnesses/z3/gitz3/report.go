package gitz3

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

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
