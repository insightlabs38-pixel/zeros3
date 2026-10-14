// Command z3car runs the Z3-14 CAR/IPLD measurements.
//
//	z3car fetch                       download + verify the pinned real samples
//	z3car families [-scale big|small|tiny] [-detail]
//	z3car real                        real go-car samples through the same matrix
//	z3car ident                       native CID identity vs SHA-256 across the corpus
//	z3car read                        one-block / range / sequential reads
//	z3car stream [-mib N] [-block B]  bounded-memory streaming over a generated CAR
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"zeros3-z3/carz3"
	"zeros3-z3/lab"
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "z3car:", err)
	os.Exit(1)
}

func cacheDir() string {
	if d := os.Getenv("Z3_CAR_CACHE"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".cache", "z3-realworld", "car")
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3car fetch|families|real|ident|read|stream [flags]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "fetch":
		err = cmdFetch(args)
	case "families":
		err = cmdFamilies(args)
	case "real":
		err = cmdReal(args)
	case "ident":
		err = cmdIdent(args)
	case "read":
		err = cmdRead(args)
	case "stream":
		err = cmdStream(args)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fatal(err)
	}
}

func scaleByName(n string) (carz3.Scale, error) {
	for _, s := range []carz3.Scale{carz3.BigBlocks, carz3.SmallBlocks, carz3.TinyBlocks} {
		if s.Name == n {
			return s, nil
		}
	}
	return carz3.Scale{}, fmt.Errorf("unknown scale %q", n)
}

func namedRevs(f carz3.Family) []lab.NamedBytes {
	var out []lab.NamedBytes
	for _, r := range f.Revs {
		out = append(out, lab.NamedBytes{Label: r.Label, Data: r.Data})
	}
	return out
}

func kib(n int64) string { return fmt.Sprintf("%.1f", float64(n)/1024) }

var tableReps = []string{"cdc", "fixed-4k", "car-section", "car-block", "car-block+recipe", "car-block+recipe-i512", "car-set", "car-auto"}

func cell(rows []lab.FRow, i int) string {
	if i >= len(rows) {
		return "-"
	}
	r := rows[i]
	switch r.Status {
	case "n/a":
		return "n/a"
	case "ok":
		if !r.Exact {
			return "INEXACT"
		}
		return fmt.Sprintf("%s (%.2f%%)", kib(r.Cost), 100*float64(r.Cost)/float64(r.Size))
	}
	return r.Status
}

func printFamily(f carz3.Family, detail bool) {
	rows := lab.RunBytesFamily(f.Name, "car", namedRevs(f), lab.CARReps())
	by := map[string][]lab.FRow{}
	for _, r := range rows {
		by[r.Rep] = append(by[r.Rep], r)
	}
	fmt.Printf("\n#### %s - %s\n\n", f.Name, f.Note)
	fmt.Printf("| revision | size KiB |")
	for _, r := range tableReps {
		fmt.Printf(" %s |", r)
	}
	fmt.Printf("\n|---|--:|%s\n", strings.Repeat("--:|", len(tableReps)))
	for i, rv := range f.Revs {
		fmt.Printf("| %s | %s |", rv.Label, kib(int64(len(rv.Data))))
		for _, rp := range tableReps {
			fmt.Printf(" %s |", cell(by[rp], i))
		}
		fmt.Println()
	}
	var sizeSum int64
	for _, rv := range f.Revs {
		sizeSum += int64(len(rv.Data))
	}
	fmt.Printf("| **cumulative** | %s |", kib(sizeSum))
	for _, rp := range tableReps {
		var c int64
		ok := true
		for _, r := range by[rp] {
			if r.Status != "ok" {
				ok = false
			}
			c += r.Cost
		}
		if !ok {
			fmt.Printf(" (n/a rows) |")
			continue
		}
		fmt.Printf(" %s (%.2f%%) |", kib(c), 100*float64(c)/float64(sizeSum))
	}
	fmt.Println()
	if !detail {
		return
	}
	fmt.Printf("\ncar-block+recipe recipe parts (bytes):\n\n| revision | new block B | header | wrapper | index | prefix | exact CID | digest table | order | recipe total | derived CIDs | index mode |\n|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|\n")
	for i, r := range by["car-block+recipe"] {
		if r.Status != "ok" {
			continue
		}
		sz := r.Detail["sizes"].(carz3.Sizes)
		st := r.Detail["stats"].(carz3.Stats)
		fmt.Printf("| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d/%d | %v |\n", f.Revs[i].Label, r.Acct.NewBytes, sz.Header, sz.Wrapper, sz.Index, sz.Prefix, sz.CIDExact, sz.BlockTbl, sz.Order, sz.Total, st.Derived, st.Sections, []string{"none", "derived", "exact"}[r.Detail["index_mode"].(int)])
	}
}

func cmdFamilies(args []string) error {
	fs := flag.NewFlagSet("families", flag.ExitOnError)
	scale := fs.String("scale", "big", "big|small|tiny")
	detail := fs.Bool("detail", false, "print recipe-part breakdown")
	only := fs.String("only", "", "substring filter on family name")
	fs.Parse(args)
	sc, err := scaleByName(*scale)
	if err != nil {
		return err
	}
	for _, f := range carz3.Families(sc) {
		if *only == "" || strings.Contains(f.Name, *only) {
			printFamily(f, *detail)
		}
	}
	return nil
}

type pinned struct {
	Name, URL, SHA256 string
	Size              int64
}

func loadPins() ([]pinned, error) {
	b, err := os.ReadFile("realworld/car.json")
	if err != nil {
		return nil, err
	}
	var f struct{ Sources []pinned }
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Sources, nil
}

func cmdFetch(args []string) error {
	pins, err := loadPins()
	if err != nil {
		return err
	}
	dir := cacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, p := range pins {
		path := filepath.Join(dir, p.Name)
		if b, err := os.ReadFile(path); err == nil && hexSum(b) == p.SHA256 {
			fmt.Printf("ok   %s (cached)\n", p.Name)
			continue
		}
		b, err := httpGet(p.URL)
		if err != nil {
			return err
		}
		if hexSum(b) != p.SHA256 || int64(len(b)) != p.Size {
			return fmt.Errorf("%s: sha256/size mismatch (got %s, %d bytes)", p.Name, hexSum(b), len(b))
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			return err
		}
		fmt.Printf("ok   %s %d bytes sha256 %s\n", p.Name, len(b), p.SHA256)
	}
	return nil
}

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func sortedKeys[V any](m map[string]V) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func sha256Of(b []byte) [32]byte { return sha256.Sum256(b) }
