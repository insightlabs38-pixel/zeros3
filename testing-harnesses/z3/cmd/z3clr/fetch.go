package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func init() { cmds["fetch"] = fetch }

type pin struct {
	Class, Family, Kind, Package, Version, URL, Member, TFM string
	PackageSHA                                              string `json:"package_sha256"`
	PackageSize                                             int64  `json:"package_size"`
	ArtifactSHA                                             string `json:"artifact_sha256"`
	ArtifactSize                                            int64  `json:"artifact_size"`
}

// fetch [-pins FILE] [-cache DIR] OUTDIR [FAMILY...] : download the pinned NuGet / npm packages
// (z3 realworld/clrwasm.json), verify both SHA-256 values and extract the artifact to
// OUTDIR/FAMILY/VERSION/BASENAME. Opt-in; never part of tests.
func fetch(args []string) error {
	pins, cache := "realworld/clrwasm.json", filepath.Join(os.TempDir(), "z3-clrwasm-cache")
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "-pins":
			pins = args[1]
		case "-cache":
			cache = args[1]
		default:
			return fmt.Errorf("unknown flag %s", args[0])
		}
		args = args[2:]
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: fetch [-pins FILE] [-cache DIR] OUTDIR [FAMILY...]")
	}
	out := args[0]
	want := map[string]bool{}
	for _, f := range args[1:] {
		want[f] = true
	}
	b, err := os.ReadFile(pins)
	if err != nil {
		return err
	}
	var doc struct{ Sources []pin }
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	os.MkdirAll(cache, 0o755)
	for _, p := range doc.Sources {
		if len(want) > 0 && !want[p.Family] {
			continue
		}
		cf := filepath.Join(cache, p.PackageSHA)
		pb, err := os.ReadFile(cf)
		if err != nil {
			resp, err := http.Get(p.URL)
			if err != nil {
				return err
			}
			pb, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || resp.StatusCode != 200 {
				return fmt.Errorf("%s: status %d err %v", p.URL, resp.StatusCode, err)
			}
			os.WriteFile(cf, pb, 0o644)
		}
		if h := sha256.Sum256(pb); hex.EncodeToString(h[:]) != p.PackageSHA {
			os.Remove(cf)
			return fmt.Errorf("%s: package hash mismatch", p.URL)
		}
		art, err := member(p, pb)
		if err != nil {
			return err
		}
		if h := sha256.Sum256(art); hex.EncodeToString(h[:]) != p.ArtifactSHA {
			return fmt.Errorf("%s %s: artifact hash mismatch", p.URL, p.Member)
		}
		dst := filepath.Join(out, p.Family, p.Version)
		os.MkdirAll(dst, 0o755)
		if err := os.WriteFile(filepath.Join(dst, path.Base(p.Member)), art, 0o644); err != nil {
			return err
		}
		fmt.Printf("%-28s %-8s %s %d\n", p.Family, p.Version, p.ArtifactSHA[:12], len(art))
	}
	return nil
}

func member(p pin, pkg []byte) ([]byte, error) {
	if p.Kind == "nuget" {
		z, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
		if err != nil {
			return nil, err
		}
		for _, f := range z.File {
			if f.Name == p.Member {
				r, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer r.Close()
				return io.ReadAll(r)
			}
		}
		return nil, fmt.Errorf("%s not in %s", p.Member, p.URL)
	}
	gz, err := gzip.NewReader(bytes.NewReader(pkg))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s not in %s", p.Member, p.URL)
		}
		if err != nil {
			return nil, err
		}
		if h.Name == p.Member {
			return io.ReadAll(tr)
		}
	}
}
