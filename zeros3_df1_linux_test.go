package main

import (
	"bytes"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"testing"
)

func TestDF1SignedReplay(t *testing.T) {
	if os.Getenv("ZEROS3_DF1_FAMILY") == "" {
		t.Skip("set ZEROS3_DF1_FAMILY to run the signed research probe")
	}
	probe := os.Getenv("ZEROS3_DF1_PROBE")
	clients, err := strconv.Atoi(os.Getenv("ZEROS3_DF1_CLIENTS"))
	mustOK(t, err)
	count, err := strconv.Atoi(os.Getenv("ZEROS3_DF1_COUNT"))
	mustOK(t, err)
	round, err := strconv.Atoi(os.Getenv("ZEROS3_DF1_ROUND"))
	mustOK(t, err)
	if clients < 1 || count < clients || (probe != "" && probe != "whole" && probe != "range4" && probe != "range64") {
		t.Fatal("invalid DF1 workload bounds")
	}
	selected := os.Getenv("ZEROS3_DF1_FAMILY")
	if selected != "curl" && selected != "lua" {
		t.Fatal("DF1 permits only the registered curl and Lua development inputs")
	}
	for _, f := range df0Corpus(t) {
		if f.Name != selected {
			continue
		}
		base := df0Object{"version/00", df0Fixture(t, f.Files[0])}
		target := df0Object{"version/01", df0Fixture(t, f.Files[1])}
		seed := df0Prepare(t, "translate", []df0Object{base, target})
		if seed == nil {
			t.Fatal("development pair did not produce a native GF7 body")
		}
		l := &authLab{t: t, dir: t.TempDir()}
		mustOK(t, os.CopyFS(l.dir, os.DirFS(seed.dir)))
		cold := os.Getenv("ZEROS3_DF1_COLD") == "1"
		maintenance := os.Getenv("ZEROS3_DF1_MAINT") == "1"
		pages := df0CachePages{}
		if cold {
			pages = df0DropPages(t, l.dir)
		}
		if maintenance {
			if cold {
				t.Fatal("maintenance run requires a warm starting store")
			}
			l.s = b4OpenMaint(t, l.dir)
			df0MaintenanceSeed(t, l)
		} else {
			l.open()
		}
		defer l.close()
		if !cold {
			// Whole priming also verifies the reconstructed object.
			get := gf2SignedReader(t, l)
			if !bytes.Equal(get(target.key, 0, 0), target.raw) {
				t.Fatal("warm replay differs")
			}
		}
		queries := df0Queries(len(target.raw), count, 123761+uint64(round))
		if probe != "" {
			for i := range queries {
				queries[i].kind = probe
				switch probe {
				case "range4":
					queries[i].n = 4096
				case "range64":
					queries[i].n = 64 << 10
				}
				if probe != "whole" {
					n := queries[i].n
					queries[i].off = (112219 + i*17381 + round*13007) % (len(target.raw) - n)
				}
			}
		}
		var before, after runtime.MemStats
		var profile *os.File
		if path := os.Getenv("ZEROS3_DF1_CPUPROFILE"); path != "" {
			profile, err = os.Create(path)
			mustOK(t, err)
			defer profile.Close()
			mustOK(t, pprof.StartCPUProfile(profile))
			defer pprof.StopCPUProfile()
		}
		runtime.ReadMemStats(&before)
		row := df0SignedRun(t, l, target, queries, clients, maintenance)
		runtime.ReadMemStats(&after)
		row["family"], row["round"], row["probe"], row["cold"] = selected, round, probe, cold
		row["cache_pages"], row["operations"] = pages, count
		row["gc_cycles"], row["gc_pause_ns"] = after.NumGC-before.NumGC, after.PauseTotalNs-before.PauseTotalNs
		row["total_objects"], row["total_bytes"] = after.Mallocs-before.Mallocs, after.TotalAlloc-before.TotalAlloc
		row["operations_per_second"] = float64(count) * 1e9 / float64(row["wall_ns"].(int64))
		df0Log(t, "DF1SIGNED", row)
		return
	}
	t.Fatal("requested development family absent from corpus")
}
