// z3fetch downloads pinned real-world research samples (realworld/sources.json)
// into a cache outside Git, verifies their SHA-256 and optionally lays out one
// group as a z3 corpus so that z3lab / z3oracle / z3deflate can run on it.
//
//	z3fetch -list
//	z3fetch -group pythia-14m -out DIR [-cache DIR] [-max-mib N]
//
// Downloads are opt-in and never part of tests. A hash mismatch is an error and
// the file is discarded.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type source struct {
	Group, Format, Name, URL, SHA256, Ref string
	Size                                  int64
}

type sources struct {
	Schema   string
	Licenses map[string]string
	Sources  []source
}

func main() {
	list := flag.Bool("list", false, "list the pinned sources")
	group := flag.String("group", "", "group to fetch")
	out := flag.String("out", "", "write the group as a z3 corpus here")
	cache := flag.String("cache", "", "download cache (default ~/.cache/z3-realworld)")
	maxMiB := flag.Int64("max-mib", 256, "refuse to download more than this in total")
	file := flag.String("sources", "realworld/sources.json", "sources file")
	flag.Parse()
	b, err := os.ReadFile(*file)
	if err != nil {
		fatal(err)
	}
	var s sources
	if err := json.Unmarshal(b, &s); err != nil {
		fatal(err)
	}
	if *list || *group == "" {
		for _, x := range s.Sources {
			fmt.Printf("%-12s %-12s %-12s %10d  %s  %s\n", x.Group, x.Format, x.Name, x.Size, x.SHA256[:12], s.Licenses[x.Group])
		}
		return
	}
	if *cache == "" {
		h, _ := os.UserHomeDir()
		*cache = filepath.Join(h, ".cache", "z3-realworld")
	}
	var sel []source
	var total int64
	for _, x := range s.Sources {
		if x.Group == *group {
			sel = append(sel, x)
			total += x.Size
		}
	}
	if len(sel) == 0 {
		fatal(fmt.Errorf("unknown group %q", *group))
	}
	if total > *maxMiB<<20 {
		fatal(fmt.Errorf("group %s is %d MiB, over -max-mib %d", *group, total>>20, *maxMiB))
	}
	type member struct {
		Family, Format, Workload, Revision, Mutation, Path, SHA256, Parent, Base string
		Index                                                                    int
		Size                                                                     int64
		Truth                                                                    map[string][]string `json:"truth,omitempty"`
	}
	var ms []member
	for i, x := range sel {
		p, err := fetch(*cache, x)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("ok %s %s (%d bytes, sha256 %s)\n", x.Group, x.Name, x.Size, x.SHA256[:12])
		if *out == "" {
			continue
		}
		ext := map[string]string{"safetensors": ".safetensors", "zip": ".zip", "gzip": ".gz"}[x.Format]
		rel := filepath.Join("rw-"+x.Group, fmt.Sprintf("r%d%s", i, ext))
		if err := os.MkdirAll(filepath.Join(*out, filepath.Dir(rel)), 0o755); err != nil {
			fatal(err)
		}
		if err := copyFile(p, filepath.Join(*out, rel)); err != nil {
			fatal(err)
		}
		m := member{Family: "rw-" + x.Group, Format: x.Format, Workload: "real-world", Revision: fmt.Sprintf("r%d", i), Index: i,
			Mutation: x.Name, Path: filepath.ToSlash(rel), Size: x.Size, SHA256: x.SHA256,
			Truth: map[string][]string{"source": {x.Ref}, "license": {s.Licenses[x.Group]}}}
		if i > 0 {
			m.Parent, m.Base = fmt.Sprintf("r%d", i-1), "r0"
		}
		ms = append(ms, m)
	}
	if *out == "" {
		return
	}
	// mirror the corpus manifest schema (lower-case JSON keys as in corpus.Member)
	type jm struct {
		Family   string              `json:"family"`
		Format   string              `json:"format"`
		Workload string              `json:"workload"`
		Revision string              `json:"revision"`
		Index    int                 `json:"index"`
		Mutation string              `json:"mutation"`
		Path     string              `json:"path"`
		Size     int64               `json:"size"`
		SHA256   string              `json:"sha256"`
		Parent   string              `json:"parent,omitempty"`
		Base     string              `json:"base,omitempty"`
		Truth    map[string][]string `json:"truth,omitempty"`
	}
	var jms []jm
	for _, m := range ms {
		jms = append(jms, jm{m.Family, m.Format, m.Workload, m.Revision, m.Index, m.Mutation, m.Path, m.Size, m.SHA256, m.Parent, m.Base, m.Truth})
	}
	man := map[string]any{"schema": "z3-corpus/1", "generator_version": 0, "go_version": "n/a (real-world)",
		"config": map[string]any{"seed": 0, "size_kib": 32, "revisions": 1, "max_total_mib": 1}, "members": jms}
	mb, _ := json.MarshalIndent(man, "", " ")
	if err := os.WriteFile(filepath.Join(*out, "manifest.json"), append(mb, '\n'), 0o644); err != nil {
		fatal(err)
	}
	fmt.Println("corpus written to", *out)
}

func fetch(cache string, x source) (string, error) {
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(cache, x.SHA256)
	if ok, _ := verify(p, x); ok {
		return p, nil
	}
	c := &http.Client{Timeout: 10 * time.Minute}
	resp, err := c.Get(x.URL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s: HTTP %d", x.URL, resp.StatusCode)
	}
	tmp := p + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, x.Size+1))
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	if n != x.Size || hex.EncodeToString(h.Sum(nil)) != x.SHA256 {
		os.Remove(tmp)
		return "", fmt.Errorf("%s: got %d bytes sha256 %x, want %d bytes %s", x.URL, n, h.Sum(nil), x.Size, x.SHA256)
	}
	return p, os.Rename(tmp, p)
}

func verify(p string, x source) (bool, error) {
	f, err := os.Open(p)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return err == nil && n == x.Size && hex.EncodeToString(h.Sum(nil)) == x.SHA256, err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "z3fetch:", err)
	os.Exit(1)
}
