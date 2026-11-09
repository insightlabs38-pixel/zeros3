// Command runner builds zeros3 once and runs selected external harness
// groups against that binary. See ../README.md.
//
//	go run ./runner -list
//	go run ./runner -group s3,sync
//	go run ./runner -group all -bin /path/to/zeros3
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type harness struct {
	pkg  string
	args []string
	need string   // env var that must be set for the harness to run
	env  []string // extra environment for the harness and the servers it starts
}

// looseLayout is the explicit loose-layout storage policy for harnesses whose
// assertions are about loose chunk files: counting chunk files, corrupting or
// deleting one, `gc` of a failed write's chunks, compacting loose chunks into
// packs. It turns the P1C small-object segments off and restores the pre-P1B
// direct-pack threshold (64 MiB). Every other harness runs the production
// default. The runner prints the policy of every harness it starts
// ("layout=loose" or "layout=production") so the coverage is visible.
var looseLayout = []string{"ZEROS3_SMALL_OBJECT_STORAGE=off", "ZEROS3_DIRECT_PACK_KIB=65536"}

func layoutOf(h harness) string {
	for _, e := range h.env {
		if e == looseLayout[0] {
			return "loose"
		}
	}
	return "production"
}

var groups = map[string][]harness{
	"static": {
		{pkg: "./harness/toc_check", args: []string{"-file", "@root/zeros3.go"}},
		{pkg: "./harness/toc_check", args: []string{"-file", "@root/zeros3_test.go"}},
	},
	"s3": {
		{pkg: "./harness/m2"}, {pkg: "./harness/m3/copy"}, {pkg: "./harness/m3/range"},
		{pkg: "./harness/m3/dedup", env: looseLayout}, {pkg: "./harness/m5a/presign"}, {pkg: "./harness/m5b/multipart"},
		{pkg: "./harness/m5d/pagination"}, {pkg: "./harness/p1/env_and_shutdown"},
		{pkg: "./harness/p2/list_multipart_uploads"}, {pkg: "./harness/m8f/conditional", env: looseLayout},
		{pkg: "./harness/z2_streaming_put"}, {pkg: "./harness/z2_aws_chunked"}, {pkg: "./harness/z2_packed_cas", env: looseLayout},
		{pkg: "./harness/z2_pack_compression", env: looseLayout},
	},
	"sync": {
		{pkg: "./harness/m6/sync"}, {pkg: "./harness/m6c/dirsync"}, {pkg: "./harness/m8a/remote_delta"},
		{pkg: "./harness/m8b/repair"}, {pkg: "./harness/m8c/namespace_replication"},
		{pkg: "./harness/m8d/fork", env: looseLayout}, {pkg: "./harness/m8e/snapshot", env: looseLayout}, {pkg: "./harness/m8g/introspection"},
	},
	"clients": {
		{pkg: "./harness/rclone", need: "RCLONE_BIN"},
		{pkg: "./harness/package-killer", need: "S3RVER_BIN"},
	},
	"client": {
		{pkg: "./profile/conformance", args: []string{"-managed", "-bin", "@bin"}},
		{pkg: "./harness/z2_consumer", args: []string{"-scenario", "invariance"}, env: looseLayout},
	},
	"apps": {
		{pkg: "./harness/z2_consumer", args: []string{"-scenario", "browser,artifact,rebalance"}},
	},
	"bench": {
		{pkg: "./harness/m8_baseline", args: []string{"-bin", "@bin"}},
		{pkg: "./harness/m8h/bench", args: []string{"-bin", "@bin"}},
		{pkg: "./harness/m8h/parallel_transfer", args: []string{"-bin", "@bin"}},
		{pkg: "./harness/z2_repack"},
	},
}

var allGroups = []string{"static", "s3", "sync", "clients"}

func main() {
	list := flag.Bool("list", false, "list groups and harnesses")
	sel := flag.String("group", "s3", "comma-separated groups: static,s3,sync,clients,client,apps,bench,all")
	bin := flag.String("bin", "", "prebuilt zeros3 binary (default: build ../zeros3.go)")
	flag.Parse()

	if *list {
		names := make([]string, 0, len(groups))
		for g := range groups {
			names = append(names, g)
		}
		sort.Strings(names)
		for _, g := range names {
			fmt.Println(g)
			for _, h := range groups[g] {
				fmt.Printf("  %s\n", h.pkg)
			}
		}
		return
	}

	root, err := filepath.Abs("..")
	if err != nil {
		fatal(err)
	}
	if *bin == "" {
		dir, err := os.MkdirTemp("", "zeros3-harness-")
		if err != nil {
			fatal(err)
		}
		defer os.RemoveAll(dir)
		*bin = filepath.Join(dir, "zeros3")
		build := exec.Command("go", "build", "-trimpath", "-o", *bin, "zeros3.go")
		build.Dir = root
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		build.Stdout, build.Stderr = os.Stdout, os.Stderr
		if err := build.Run(); err != nil {
			fatal(fmt.Errorf("build zeros3: %w", err))
		}
	}
	absBin, err := filepath.Abs(*bin)
	if err != nil {
		fatal(err)
	}

	var names []string
	for _, g := range strings.Split(*sel, ",") {
		if g = strings.TrimSpace(g); g == "all" {
			names = append(names, allGroups...)
		} else if _, ok := groups[g]; ok {
			names = append(names, g)
		} else {
			fatal(fmt.Errorf("unknown group %q (see -list)", g))
		}
	}

	var failed, skipped, ran int
	start := time.Now()
	for _, g := range names {
		for _, h := range groups[g] {
			if h.need != "" && os.Getenv(h.need) == "" {
				fmt.Printf("SKIP %-45s (%s not set)\n", h.pkg, h.need)
				skipped++
				continue
			}
			args := []string{"run", h.pkg}
			for _, a := range h.args {
				a = strings.ReplaceAll(a, "@root", root)
				args = append(args, strings.ReplaceAll(a, "@bin", absBin))
			}
			cmd := exec.Command("go", args...)
			cmd.Env = append(append(cleanEnv(), "ZEROS3_BIN="+absBin), h.env...)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			t := time.Now()
			fmt.Printf("=== %s (layout=%s)\n", h.pkg, layoutOf(h))
			status := "PASS"
			if err := cmd.Run(); err != nil {
				status = "FAIL"
				failed++
			}
			ran++
			fmt.Printf("%s %-45s layout=%-10s %s\n", status, h.pkg, layoutOf(h), time.Since(t).Round(time.Millisecond))
		}
	}
	fmt.Printf("\n%d run, %d failed, %d skipped in %s\n", ran, failed, skipped, time.Since(start).Round(time.Second))
	if failed > 0 {
		os.Exit(1)
	}
}

// cleanEnv drops ambient AWS_* settings so they cannot override the
// credentials the harnesses and zeros3 default to.
func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "AWS_") {
			env = append(env, kv)
		}
	}
	return env
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "runner:", err)
	os.Exit(2)
}
