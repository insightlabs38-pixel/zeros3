package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"zeros3-testing/profile/core"
)

// artifact: a checkpoint-like 24 MiB file uploaded by multipart, ranged,
// locally edited and delta-synced, snapshotted, tier-moved and re-read.
func artifact() {
	const prof = "z2-consumer/artifact"
	e := newEnv("artifact")
	defer e.close()
	e.start()
	t := core.NewT(e.aws, e.cfg, "artifact")
	must(e.aws.CreateBucket("models"))

	// Layers: alternating dense random "weights" and compressible "buffers".
	v1 := make([]byte, 0, 24<<20)
	for i := 0; len(v1) < 24<<20; i++ {
		if i%2 == 0 {
			v1 = append(v1, core.Data(byte(20+i), 1<<20)...)
		} else {
			v1 = append(v1, bytesRepeat(fmt.Sprintf("layer-%d-norm-stats\n", i), 1<<20)...)
		}
	}
	id, err := e.aws.MPCreate("models", "ckpt/model-v1.bin", core.PutOpts{})
	must(err)
	var parts []core.Part
	for n, off := 1, 0; off < len(v1); n, off = n+1, off+5<<20 {
		etag, err := e.aws.MPPart("models", "ckpt/model-v1.bin", id, n, v1[off:min(off+5<<20, len(v1))])
		must(err)
		parts = append(parts, core.Part{N: n, ETag: etag})
	}
	etag, err := e.aws.MPComplete("models", "ckpt/model-v1.bin", id, parts)
	t.Check("multipart checkpoint upload", err == nil && strings.HasSuffix(etag, fmt.Sprintf("-%d", len(parts))), "%q %v", etag, err)
	rg, err := e.aws.Get("models", "ckpt/model-v1.bin", core.GetOpts{Range: "bytes=7340000-7440000"})
	t.Check("range read across a part boundary", err == nil && bytes.Equal(rg.Body, v1[7340000:7440001]), "%v", err)

	// Localized edit: 64 KiB rewritten mid-file, delta-synced by ZeroS3's CLI.
	v2 := append([]byte{}, v1...)
	copy(v2[9_300_000:], core.Data(77, 64<<10))
	lf, err := os.CreateTemp("", "zeros3-model-v2-*.bin")
	must(err)
	local := lf.Name()
	defer os.Remove(local)
	_, err = lf.Write(v2)
	must(err)
	must(lf.Close())
	before := e.stats()
	out, err := exec.Command(bin, "sync", "-endpoint", e.cfg.Endpoint, local, "s3://models/ckpt/model-v2.bin").CombinedOutput()
	must(err)
	_ = out
	after := e.stats()
	grew := after.LooseChunkFileBytes - before.LooseChunkFileBytes
	t.Check("delta sync stores only the edited region (<1 MiB new chunk bytes)", grew > 0 && grew < 1<<20, "new chunk bytes %d for a %d byte file", grew, len(v2))
	g, err := e.aws.Get("models", "ckpt/model-v2.bin", core.GetOpts{})
	t.Check("synced v2 reads back byte-exact", err == nil && bytes.Equal(g.Body, v2), "%v", err)

	// Snapshot, then pack and tier-move; readback is unchanged.
	var snap struct {
		SnapshotID string `json:"snapshot_id"`
	}
	e.cli(&snap, "snapshot", "create", "-endpoint", e.cfg.Endpoint, "-json", "s3://models/")
	t.Check("snapshot created", snap.SnapshotID != "", "%+v", snap)
	r := t.Report()
	r.Profile = prof
	record(r)
	e.stop()
	e.cli(nil, "compact", "-store", e.store, "-pack-size-mib", "4", "-json")
	e.cli(nil, "tier", "move", "-store", e.store, "-from", "hot", "-to", "warm", "-all", "-apply", "-json")
	e.cli(nil, "verify", "-store", e.store, "-deep")
	e.start()
	t = core.NewT(e.aws, e.cfg, "artifact-tiered")
	for _, c := range []core.Client{e.aws, e.mc} {
		for k, want := range map[string][]byte{"ckpt/model-v1.bin": v1, "ckpt/model-v2.bin": v2} {
			g, err := c.Get("models", k, core.GetOpts{})
			t.Check(c.Name()+" warm-tier GET "+k, err == nil && bytes.Equal(g.Body, want), "%v", err)
			rg, err := c.Get("models", k, core.GetOpts{Range: "bytes=9290000-9400000"})
			t.Check(c.Name()+" warm-tier range "+k, err == nil && bytes.Equal(rg.Body, want[9290000:9400001]), "%v", err)
		}
	}
	r = t.Report()
	r.Profile = prof + "/tiered"
	record(r)
}
