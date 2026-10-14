// z3fetch downloads pinned real-world research samples (realworld/sources.json)
// into a cache outside Git, verifies their SHA-256 and optionally lays out one
// group as a z3 corpus so that z3lab / z3oracle / z3deflate can run on it.
//
//	z3fetch -list
//	z3fetch -group pythia-14m -out DIR [-cache DIR] [-max-mib N]
//	z3fetch -group go-src,django-sdist -out DIR     (comma list; "deflate" = every gzip/zip group)
//
// URLs of the form oci://REGISTRY/REPO@sha256:DIGEST fetch a registry blob with
// an anonymous Docker Hub pull token (the blob digest is also its SHA-256).
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
	"strings"
	"time"
)

type source struct {
	Group, Format, Name, URL, SHA256, Ref, Upstream string
	Size                                            int64
}

type sources struct {
	Schema   string
	Licenses map[string]string
	Sources  []source
}

func main() {
	list := flag.Bool("list", false, "list the pinned sources")
	group := flag.String("group", "", "group(s) to fetch (comma list; 'deflate' = all gzip/zip groups)")
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
			fmt.Printf("%-18s %-12s %-14s %10d  %s  %s\n", x.Group, x.Format, x.Name, x.Size, x.SHA256[:12], s.Licenses[x.Group])
		}
		return
	}
	if *cache == "" {
		h, _ := os.UserHomeDir()
		*cache = filepath.Join(h, ".cache", "z3-realworld")
	}
	var sel []source
	var total int64
	want := map[string]bool{}
	for _, g := range strings.Split(*group, ",") {
		want[g] = true
	}
	for _, x := range s.Sources {
		if want[x.Group] || want["deflate"] && (x.Format == "gzip" || x.Format == "zip") {
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
	nth := map[string]int{}
	for _, x := range sel {
		p, err := fetch(*cache, x)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("ok %s %s (%d bytes, sha256 %s)\n", x.Group, x.Name, x.Size, x.SHA256[:12])
		if *out == "" {
			continue
		}
		ext := map[string]string{"safetensors": ".safetensors", "zip": ".zip", "gzip": ".gz"}[x.Format]
		idx := nth[x.Group]
		nth[x.Group]++
		rel := filepath.Join("rw-"+x.Group, fmt.Sprintf("r%d%s", idx, ext))
		if err := os.MkdirAll(filepath.Join(*out, filepath.Dir(rel)), 0o755); err != nil {
			fatal(err)
		}
		if err := copyFile(p, filepath.Join(*out, rel)); err != nil {
			fatal(err)
		}
		m := member{Family: "rw-" + x.Group, Format: x.Format, Workload: "real-world", Revision: fmt.Sprintf("r%d", idx), Index: idx,
			Mutation: x.Name, Path: filepath.ToSlash(rel), Size: x.Size, SHA256: x.SHA256,
			Truth: map[string][]string{"source": {x.Ref}, "license": {s.Licenses[x.Group]}}}
		if idx > 0 {
			m.Parent, m.Base = fmt.Sprintf("r%d", idx-1), "r0"
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
	resp, err := get(c, x.URL)
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

// get issues the request; oci:// URLs go through an anonymous pull token and
// the registry's redirect (the Authorization header is not forwarded).
func get(c *http.Client, u string) (*http.Response, error) {
	if !strings.HasPrefix(u, "oci://") {
		return c.Get(u)
	}
	host, path, ok := strings.Cut(strings.TrimPrefix(u, "oci://"), "/")
	repo, digest, ok2 := strings.Cut(path, "@")
	if !ok || !ok2 || host != "registry-1.docker.io" {
		return nil, fmt.Errorf("unsupported oci url %q", u)
	}
	tr, err := c.Get("https://auth.docker.io/token?service=registry.docker.io&scope=repository:" + repo + ":pull")
	if err != nil {
		return nil, err
	}
	defer tr.Body.Close()
	var tok struct{ Token string }
	if err := json.NewDecoder(tr.Body).Decode(&tok); err != nil || tok.Token == "" {
		return nil, fmt.Errorf("oci token: %v", err)
	}
	req, _ := http.NewRequest("GET", "https://"+host+"/v2/"+repo+"/blobs/"+digest, nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	return c.Do(req)
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
