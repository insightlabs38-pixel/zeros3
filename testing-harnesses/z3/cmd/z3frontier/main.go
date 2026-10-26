// Command z3frontier runs the Z3-23 (content-aware container formats) and Z3-24
// (read-only filesystem images) characterisations over one shared deterministic
// source-tree revision corpus. External producers live outside Git (see
// oracle/containers/setup.sh); results are written under -out, never committed.
//
//	z3frontier gen -rev R3 -dir DIR     materialise one revision and print its digest
//	z3frontier c23 [flags]              Z3-23 container-format chain
//	z3frontier i24 [flags]              Z3-24 filesystem-image chain
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"zeros3-z3/frontier"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z3frontier:", err)
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3frontier <gen|c23|i24> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "gen":
		fs := flag.NewFlagSet("gen", flag.ExitOnError)
		rev := fs.String("rev", "R0", "revision")
		dir := fs.String("dir", "", "target directory")
		fs.Parse(os.Args[2:])
		t0 := time.Now()
		t := frontier.Revision(frontier.Base(), *rev)
		fmt.Println(t.Rev, t.Stats())
		must(frontier.Materialize(t, *dir))
		snap, err := frontier.Snapshot(*dir, true)
		must(err)
		fmt.Printf("digest=%s entries=%d in %v\n", SnapDigest(snap), len(snap), time.Since(t0).Round(time.Millisecond))
	case "c23":
		cmdC23(os.Args[2:])
	case "estargz-replay":
		cmdEstargzReplay(os.Args[2:])
	case "i24":
		cmdI24(os.Args[2:])
	case "i24x":
		cmdI24Cross(os.Args[2:])
	case "dupes":
		cmdDupes(os.Args[2:])
	case "i24sum":
		cmdI24Sum(os.Args[2:])
	case "verify":
		cmdVerify(os.Args[2:])
	case "scan":
		cmdScan(os.Args[2:])
	case "c23sum":
		cmdC23Sum(os.Args[2:])
	case "tar":
		fs := flag.NewFlagSet("tar", flag.ExitOnError)
		rev := fs.String("rev", "R0", "revision")
		dir := fs.String("dir", "", "materialised directory")
		out := fs.String("out", "", "output tar")
		fs.Parse(os.Args[2:])
		t := frontier.Revision(frontier.Base(), *rev)
		f, err := os.Create(*out)
		must(err)
		must(frontier.WriteTar(f, t, *dir))
		must(f.Close())
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}

// SnapDigest hashes a snapshot (paths, types, modes, sizes, mtimes, links, content, xattrs).
func SnapDigest(s map[string]frontier.Snap) string {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		v := s[k]
		fmt.Fprintf(h, "%s|%s|%o|%d|%d|%s|%s|%s|%s\n", k, v.Type, v.Mode, v.Size, v.Mtime, v.SHA, v.Link, v.Hard, v.Xattr)
	}
	return hex.EncodeToString(h.Sum(nil))
}
