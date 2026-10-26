// Command z3zstd runs the Z3-25 exact-Zstandard experiments over the shared Z3-23
// R0-R11 revision corpus. External zstd builds live outside Git (oracle/zstd/setup.sh);
// scratch results are written under -dir and never committed.
//
//	z3zstd gen   -dir D [-tags z155l3,...] [-revs R0,...]   materialise, tar, compress, floor
//	z3zstd eco   -dir D -tag T                               Z3-25 economics over the chain
package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z3zstd:", err)
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3zstd <gen|eco|...> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "gen":
		cmdGen(os.Args[2:])
	case "inspect":
		cmdInspect(os.Args[2:])
	case "predict":
		cmdPredict(os.Args[2:])
	case "memgen":
		cmdMemGen(os.Args[2:])
	case "memparse":
		cmdMemParse(os.Args[2:])
	case "memreplay":
		cmdMemReplay(os.Args[2:])
	case "units":
		cmdUnits(os.Args[2:])
	case "unitsim":
		cmdUnitSim(os.Args[2:])
	case "matrix":
		cmdMatrix(os.Args[2:])
	case "range":
		cmdRange(os.Args[2:])
	case "repz":
		cmdRepz(os.Args[2:])
	case "sqfs":
		cmdSqfs(os.Args[2:])
	case "ecosum":
		cmdEcoSum(os.Args[2:])
	case "eco":
		cmdEco(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}

// producer is one pinned external zstd invocation. Everything is single-threaded
// unless the tag says otherwise, because producer defaults may change.
type producer struct {
	Tag  string
	Ver  string
	Args []string
}

var producers = map[string]producer{
	"z155l3":  {"z155l3", "1.5.5", []string{"-3", "-T1"}},
	"z157l3":  {"z157l3", "1.5.7", []string{"-3", "-T1"}},
	"z157l9":  {"z157l9", "1.5.7", []string{"-9", "-T1"}},
	"z155l1":  {"z155l1", "1.5.5", []string{"-1", "-T1"}},
	"z157l1":  {"z157l1", "1.5.7", []string{"-1", "-T1"}},
	"z155l19": {"z155l19", "1.5.5", []string{"-19", "-T1"}},
	"z157l19": {"z157l19", "1.5.7", []string{"-19", "-T1"}},
	"z155f3":  {"z155f3", "1.5.5", []string{"--fast=3", "-T1"}},
	"z157f3":  {"z157f3", "1.5.7", []string{"--fast=3", "-T1"}},
	"z157mt":  {"z157mt", "1.5.7", []string{"-3", "-T4", "-B1048576"}},
}

func toolsRoot() string {
	if r := os.Getenv("Z3_TOOLS"); r != "" {
		return r
	}
	return "/root/z3cache"
}

func zstdBin(ver string) string { return filepath.Join(toolsRoot(), "tools", "zstd-"+ver, "zstd") }

func (p producer) compress(in, out string) error {
	args := append(append([]string{}, p.Args...), "-q", "-f", in, "-o", out)
	cmd := exec.Command(zstdBin(p.Ver), args...)
	var eb bytes.Buffer
	cmd.Stderr = &eb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w\n%s", p.Ver, args, err, eb.String())
	}
	return nil
}

func (p producer) cmdline() string {
	return "zstd-" + p.Ver + " " + strings.Join(p.Args, " ") + " -q -f IN -o OUT"
}

func fileSHA(p string) string {
	b, err := os.ReadFile(p)
	must(err)
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h)
}

func since(t time.Time) time.Duration { return time.Since(t).Round(time.Millisecond) }
