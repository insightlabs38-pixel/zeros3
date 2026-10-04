package main

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"zeros3-testing/profile/core"
)

type rebalanceJSON struct {
	LiveSetOK               bool             `json:"live_set_ok"`
	ChunksByTarget          map[string]int64 `json:"chunks_by_target"`
	LogicalBytesByTarget    map[string]int64 `json:"logical_bytes_by_target"`
	ChunksPromotedBySharing int64            `json:"chunks_promoted_by_sharing"`
	PacksCompliant          int              `json:"packs_compliant"`
	PacksWholeMove          int              `json:"packs_whole_move"`
	PacksRewrite            int              `json:"packs_rewrite"`
	PacksRedundant          int              `json:"packs_redundant"`
	PacksDeferred           int              `json:"packs_deferred"`
	MisplacedChunks         int64            `json:"misplaced_chunks"`
	MisplacedLogicalBytes   int64            `json:"misplaced_logical_bytes"`
	BytesRead               int64            `json:"bytes_read"`
	BytesWritten            int64            `json:"bytes_written"`
	WriteAmplification      float64          `json:"write_amplification"`
	PacksMoved              int              `json:"packs_moved"`
	PacksRewritten          int              `json:"packs_rewritten"`
	Steps                   []struct{}       `json:"steps"`
}

type tierStatusJSON struct {
	LooseBytes int64 `json:"loose_bytes"`
	Tiers      []struct {
		Tier              string `json:"tier"`
		PackFileBytes     int64  `json:"pack_file_bytes"`
		PackedLiveLogical int64  `json:"packed_live_logical_bytes"`
		LiveChunks        int    `json:"packed_live_chunk_count"`
	} `json:"tiers"`
}

func (e *env) rebalance(apply bool) (r rebalanceJSON, took time.Duration) {
	args := []string{"tier", "rebalance", "-store", e.store, "-pack-size-mib", "1", "-json"}
	if apply {
		args = append(args, "-apply")
	}
	start := time.Now()
	e.cli(&r, args...)
	return r, time.Since(start)
}

func (e *env) tierLiveBytes() (m map[string]int64, ts tierStatusJSON) {
	e.cli(&ts, "tier", "status", "-store", e.store, "-json")
	m = map[string]int64{"hot": ts.LooseBytes}
	for _, t := range ts.Tiers {
		m[t.Tier] += t.PackedLiveLogical
	}
	return m, ts
}

func measure(format string, args ...any) { fmt.Fprintf(os.Stderr, "measure: "+format+"\n", args...) }

// rebalance: content-aware placement over the browser workload (shared
// chunks stay hot, snapshot-only warm, history-only cold, reads unchanged,
// policy and prune change placement) and a checkpoint projection (shared model
// blocks are never duplicated into a colder tier).
func rebalance() {
	const prof = "z2-consumer/rebalance"
	e := newEnv("rebalance")
	defer e.close()
	e.start()
	t := core.NewT(e.aws, e.cfg, "rebalance")
	must(e.aws.CreateBucket("web"))
	a := siteA()
	// An early draft of two files: its replaced chunks are referenced by history only.
	for _, k := range []string{"site/js/vendor.js", "site/css/app.css"} {
		put(t, e.aws, "web", k, insertAt(a[k], 50<<10, "/* draft */"), contentType(k))
	}
	for _, k := range sortedKeys(a) {
		put(t, e.aws, "web", k, a[k], contentType(k))
	}
	var snap struct {
		SnapshotID string `json:"snapshot_id"`
	}
	e.cli(&snap, "snapshot", "create", "-endpoint", e.cfg.Endpoint, "-json", "s3://web/")
	b := siteB(a)
	for _, k := range sortedKeys(b) {
		if old, ok := a[k]; !ok || !bytes.Equal(old, b[k]) {
			put(t, e.aws, "web", k, b[k], contentType(k))
		}
	}
	for _, k := range []string{"site/js/app.v1.js", "site/legacy/old.css"} {
		must(e.aws.Delete("web", k))
	}
	e.stop()
	e.cli(nil, "compact", "-store", e.store, "-pack-size-mib", "1", "-json")

	e.start()
	tb := core.NewT(e.aws, e.cfg, "rebalance-before")
	before := observe(tb, []core.Client{e.aws, e.mc}, "web", b)
	e.stop()
	physical0, _ := e.tierLiveBytes()

	plan, planTook := e.rebalance(false)
	note(prof, "dry-run finds hot, warm and cold targets and chunks promoted by sharing",
		plan.LiveSetOK && plan.ChunksByTarget["hot"] > 0 && plan.ChunksByTarget["warm"] > 0 && plan.ChunksByTarget["cold"] > 0 && plan.ChunksPromotedBySharing > 0, "%+v", plan)
	note(prof, "dry-run changed nothing", func() bool {
		m, _ := e.tierLiveBytes()
		return m["hot"] == physical0["hot"] && m["warm"] == 0 && m["cold"] == 0
	}(), "%v", physical0)
	measure("policy planning: %v for %d live chunks (%d logical bytes)", planTook, plan.ChunksByTarget["hot"]+plan.ChunksByTarget["warm"]+plan.ChunksByTarget["cold"],
		plan.LogicalBytesByTarget["hot"]+plan.LogicalBytesByTarget["warm"]+plan.LogicalBytesByTarget["cold"])

	res, took := e.rebalance(true)
	phys, ts := e.tierLiveBytes()
	note(prof, "apply places chunks: hot keeps shared+current, warm snapshot-only, cold history-only",
		phys["hot"] > 0 && phys["warm"] > 0 && phys["cold"] > 0, "live logical bytes per tier %v vs targets %v", phys, plan.LogicalBytesByTarget)
	// Chunks live loose or packed in exactly one place after convergence, so
	// the per-tier totals equal the planner's per-target totals.
	for _, tier := range []string{"warm", "cold"} {
		note(prof, tier+" tier holds exactly its target bytes", phys[tier] == plan.LogicalBytesByTarget[tier], "%d vs %d", phys[tier], plan.LogicalBytesByTarget[tier])
	}
	measure("browser rebalance apply: %v | %d whole-pack moves, %d packs rewritten | read %d, wrote %d bytes for %d misplaced logical bytes (write amplification %.2f) | physical per tier %v",
		took, res.PacksMoved, res.PacksRewritten, res.BytesRead, res.BytesWritten, plan.MisplacedLogicalBytes, res.WriteAmplification, func() []int64 {
			var v []int64
			for _, x := range ts.Tiers {
				v = append(v, x.PackFileBytes)
			}
			return v
		}())
	again, _ := e.rebalance(false)
	note(prof, "a converged store has nothing left to move", len(again.Steps) == 0 && again.MisplacedChunks == 0, "%+v", again)
	e.cli(nil, "verify", "-store", e.store, "-deep")

	e.start()
	ta := core.NewT(e.aws, e.cfg, "rebalance-after")
	after := observe(ta, []core.Client{e.aws, e.mc}, "web", b)
	r := tb.Report()
	r.Profile = prof + "/before"
	record(r)
	r = ta.Report()
	r.Profile = prof + "/after"
	record(r)
	e.stop()
	note(prof, "GET/Range/HEAD/conditional observations are identical before and after rebalance", before == after, "%s vs %s", before, after)

	// Policy changes placement on the next rebalance: history to warm empties cold.
	e.cli(nil, "tier", "policy", "set", "-store", e.store, "-scope", "history", "-tier", "warm", "-json")
	pol, _ := e.rebalance(false)
	note(prof, "history->warm override leaves no cold target", pol.ChunksByTarget["cold"] == 0, "%+v", pol.ChunksByTarget)
	e.cli(nil, "tier", "policy", "remove", "-store", e.store, "-scope", "history", "-json")

	// Pruning the roots changes the effective placement.
	e.cli(nil, "versions", "prune", "-store", e.store, "-bucket", "web", "-keep-last", "0", "-apply", "-json")
	pruned, _ := e.rebalance(false)
	note(prof, "after history prune no chunk targets cold", pruned.ChunksByTarget["cold"] == 0, "%+v", pruned.ChunksByTarget)
	e.cli(nil, "tier", "rebalance", "-store", e.store, "-pack-size-mib", "1", "-apply", "-json")
	e.cli(nil, "gc", "-store", e.store, "-apply", "-json")
	e.cli(nil, "verify", "-store", e.store, "-deep")

	checkpointProjection(prof)
}

// checkpointProjection: latest/ stays hot, the older checkpoint is warm, and
// the blocks they share are kept hot once instead of being copied warm too.
func checkpointProjection(prof string) {
	prof += "/checkpoint"
	e := newEnv("rebalance-ckpt")
	defer e.close()
	e.start()
	t := core.NewT(e.aws, e.cfg, "rebalance-ckpt")
	must(e.aws.CreateBucket("models"))
	old := core.Data(40, 16<<20)
	latest := append([]byte{}, old...)
	copy(latest[9_000_000:], core.Data(41, 256<<10)) // a localized update
	put(t, e.aws, "models", "ckpt/old/model.bin", old, "application/octet-stream")
	put(t, e.aws, "models", "ckpt/latest/model.bin", latest, "application/octet-stream")
	e.stop()
	e.cli(nil, "compact", "-store", e.store, "-pack-size-mib", "4", "-json")
	e.cli(nil, "tier", "policy", "set", "-store", e.store, "-scope", "current", "-bucket", "models", "-prefix", "ckpt/latest/", "-tier", "hot", "-json")
	e.cli(nil, "tier", "policy", "set", "-store", e.store, "-scope", "current", "-bucket", "models", "-prefix", "ckpt/old/", "-tier", "warm", "-json")
	var stats statsJSON
	e.cli(&stats, "stats", "-store", e.store, "-json")
	plan, planTook := e.rebalance(false)
	var capped rebalanceJSON
	e.cli(&capped, "tier", "rebalance", "-store", e.store, "-pack-size-mib", "4", "-min-misplaced-percent", "10", "-json")
	res, took := e.rebalance(true)
	phys, _ := e.tierLiveBytes()
	var total int64
	for _, v := range phys {
		total += v
	}
	note(prof, "shared blocks are promoted hot rather than duplicated warm", plan.ChunksPromotedBySharing > 0, "%+v", plan)
	note(prof, "every live block has exactly one physical home (no duplicates across tiers)", total == stats.UniqueReachableChunkBytes, "tiers %v total %d unique %d", phys, total, stats.UniqueReachableChunkBytes)
	note(prof, "only the old checkpoint's own blocks are warm", phys["warm"] > 0 && phys["warm"] < 2<<20 && phys["hot"] >= int64(len(latest)), "%v", phys)
	measure("checkpoint rebalance: plan %v, apply %v | %d whole moves, %d rewrites | wrote %d bytes for %d misplaced (amplification %.2f) | live per tier %v",
		planTook, took, res.PacksMoved, res.PacksRewritten, res.BytesWritten, plan.MisplacedLogicalBytes, res.WriteAmplification, phys)
	measure("checkpoint with -min-misplaced-percent 10 would instead defer %d pack(s) and write %d bytes (vs %d) leaving %d misplaced logical bytes", capped.PacksDeferred, capped.BytesWritten, plan.BytesWritten, capped.MisplacedLogicalBytes)
	e.cli(nil, "verify", "-store", e.store, "-deep")
	e.start()
	t = core.NewT(e.aws, e.cfg, "rebalance-ckpt-final")
	for k, want := range map[string][]byte{"ckpt/old/model.bin": old, "ckpt/latest/model.bin": latest} {
		g, err := e.aws.Get("models", k, core.GetOpts{})
		t.Check("GET "+k+" after rebalance", err == nil && bytes.Equal(g.Body, want), "%v", err)
		rg, err := e.aws.Get("models", k, core.GetOpts{Range: "bytes=9000000-9300000"})
		t.Check("range "+k+" after rebalance", err == nil && bytes.Equal(rg.Body, want[9000000:9300001]), "%v", err)
	}
	r := t.Report()
	r.Profile = prof
	record(r)
}
