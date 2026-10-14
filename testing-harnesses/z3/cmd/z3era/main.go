// Command z3era runs the Z3-15C EraE measurements.
//
//	z3era fetch                 download + verify the pinned archives
//	z3era characterize          per-file entry/byte/compression breakdown
//	z3era families [-tiny]      adjacent / overlapping / re-exported archive families
//	z3era read                  seek one component, a range, the whole file
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"zeros3-z3/eraez3"
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "z3era:", err)
	os.Exit(1)
}

func cacheDir() string {
	if d := os.Getenv("Z3_ERAE_CACHE"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".cache", "z3-realworld", "erae")
}

func load(name string) []byte {
	b, err := os.ReadFile(filepath.Join(cacheDir(), name))
	if err != nil {
		fatal(fmt.Errorf("%v (run `z3era fetch`)", err))
	}
	return b
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3era fetch|characterize|families|read")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "fetch":
		err = cmdFetch()
	case "characterize":
		err = cmdCharacterize()
	case "families":
		err = cmdFamilies(os.Args[2:])
	case "read":
		err = cmdRead()
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fatal(err)
	}
}

func cmdFetch() error {
	b, err := os.ReadFile("realworld/erae.json")
	if err != nil {
		return err
	}
	var f struct {
		Sources []struct {
			Name, URL, SHA256 string
			Size              int64
		}
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir(), 0o755); err != nil {
		return err
	}
	for _, s := range f.Sources {
		path := filepath.Join(cacheDir(), s.Name)
		if d, err := os.ReadFile(path); err == nil && hexSum(d) == s.SHA256 {
			fmt.Printf("ok   %s (cached)\n", s.Name)
			continue
		}
		c := &http.Client{Timeout: 120 * time.Second}
		resp, err := c.Get(s.URL)
		if err != nil {
			return err
		}
		d, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			return fmt.Errorf("%s: %v %s", s.Name, err, resp.Status)
		}
		if hexSum(d) != s.SHA256 || int64(len(d)) != s.Size {
			return fmt.Errorf("%s: sha256/size mismatch", s.Name)
		}
		if err := os.WriteFile(path, d, 0o644); err != nil {
			return err
		}
		fmt.Printf("ok   %s %d bytes\n", s.Name, len(d))
	}
	return nil
}

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func kib(n int64) string { return fmt.Sprintf("%.1f", float64(n)/1024) }

func typeName(t uint16) string {
	switch t {
	case eraez3.TypeVersion:
		return "Version"
	case eraez3.TypeHeader:
		return "Header"
	case eraez3.TypeBody:
		return "Body"
	case eraez3.TypeSlimReceipts:
		return "SlimReceipts"
	case eraez3.TypeTD:
		return "TotalDifficulty"
	case eraez3.TypeAccumulator:
		return "Accumulator"
	case eraez3.TypeComponentIndex:
		return "ComponentIndex"
	}
	return fmt.Sprintf("%#x", t)
}

func cmdCharacterize() error {
	for _, name := range []string{"sepolia-00000-8e3e7dc9.erae", "sepolia-00003-47e8d303.erae", "mainnet-00000-a6860fef.erae", "mainnet-00001-05c64fc4.erae"} {
		f := load(name)
		ents, err := eraez3.Parse(bytes.NewReader(f), int64(len(f)), eraez3.DefaultLimits())
		if err != nil {
			return err
		}
		type agg struct {
			n           int
			file, plain int64
			distinct    map[string]bool
		}
		by := map[uint16]*agg{}
		for _, e := range ents {
			a := by[e.Type]
			if a == nil {
				a = &agg{distinct: map[string]bool{}}
				by[e.Type] = a
			}
			p := f[e.Off+8 : e.End()]
			a.n++
			a.file += 8 + int64(e.Len)
			a.distinct[string(p)] = true
			switch e.Type {
			case eraez3.TypeHeader, eraez3.TypeBody, eraez3.TypeSlimReceipts:
				pl, _ := eraez3.FrameDecode(p)
				a.plain += int64(len(pl))
			default:
				a.plain += int64(len(p))
			}
		}
		fmt.Printf("\n#### %s (%d bytes, %d entries)\n\n| entry type | entries | file bytes | share | plaintext bytes | distinct payloads |\n|---|--:|--:|--:|--:|--:|\n", name, len(f), len(ents))
		var ts []int
		for t := range by {
			ts = append(ts, int(t))
		}
		sort.Ints(ts)
		for _, ti := range ts {
			a := by[uint16(ti)]
			fmt.Printf("| %s | %d | %d | %.1f%% | %d | %d |\n", typeName(uint16(ti)), a.n, a.file, 100*float64(a.file)/float64(len(f)), a.plain, len(a.distinct))
		}
		rep, st, err := eraez3.Build(bytes.NewReader(f), int64(len(f)), eraez3.DefaultLimits(), true)
		if err != nil {
			return err
		}
		var allPlain int
		for _, s := range rep.Streams {
			allPlain += len(s)
		}
		fmt.Printf("\nframed entries proven canonical by the reference encoder: %d of %d (non-canonical %d); component index %d B, derived exactly: %v; layout %+v\n", st.Plain, st.Plain+st.NonCanonical, st.NonCanonical, st.IndexBytes, st.IndexDerived, rep.Layout)
	}
	return nil
}
