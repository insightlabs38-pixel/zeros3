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
	need string // env var that must be set for the harness to run
}

var groups = map[string][]harness{
	"static": {
		{pkg: "./harness/toc_check", args: []string{"-file", "@root/zeros3.go"}},
		{pkg: "./harness/toc_check", args: []string{"-file", "@root/zeros3_test.go"}},
	},
	"s3": {
		{pkg: "./harness/m2"}, {pkg: "./harness/m3/copy"}, {pkg: "./harness/m3/range"},
		{pkg: "./harness/m3/dedup"}, {pkg: "./harness/m5a/presign"}, {pkg: "./harness/m5b/multipart"},
		{pkg: "./harness/m5d/pagination"}, {pkg: "./harness/p1/env_and_shutdown"},
		{pkg: "./harness/p2/list_multipart_uploads"}, {pkg: "./harness/m8f/conditional"},
		{pkg: "./harness/z2_streaming_put"}, {pkg: "./harness/z2_aws_chunked"}, {pkg: "./harness/z2_packed_cas"},
		{pkg: "./harness/z2_pack_compression"},
	},
	"sync": {
		{pkg: "./harness/m6/sync"}, {pkg: "./harness/m6c/dirsync"}, {pkg: "./harness/m8a/remote_delta"},
		{pkg: "./harness/m8b/repair"}, {pkg: "./harness/m8c/namespace_replication"},
		{pkg: "./harness/m8d/fork"}, {pkg: "./harness/m8e/snapshot"}, {pkg: "./harness/m8g/introspection"},
	},
	"clients": {
		{pkg: "./harness/rclone", need: "RCLONE_BIN"},
		{pkg: "./harness/package-killer", need: "S3RVER_BIN"},
	},
	"client": {
		{pkg: "./profile/conformance", args: []string{"-managed", "-bin", "@bin"}},
		{pkg: "./harness/z2_consumer", args: []string{"-scenario", "invariance"}},
	},
	"apps": {
		{pkg: "./harness/z2_consumer", args: []string{"-scenario", "browser,artifact"}},
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
			cmd.Env = append(cleanEnv(), "ZEROS3_BIN="+absBin)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			t := time.Now()
			fmt.Printf("=== %s\n", h.pkg)
			status := "PASS"
			if err := cmd.Run(); err != nil {
				status = "FAIL"
				failed++
			}
			ran++
			fmt.Printf("%s %-45s %s\n", status, h.pkg, time.Since(t).Round(time.Millisecond))
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
