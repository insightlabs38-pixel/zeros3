package main

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"zeros3-testing/profile/core"
)

// wordSalad is deterministic, compressible-but-not-trivial text.
func wordSalad(seed uint32, n int, vocab []string, sep string) []byte {
	var b bytes.Buffer
	x := seed*2654435761 + 7
	for b.Len() < n {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b.WriteString(vocab[x%uint32(len(vocab))])
		b.WriteString(sep)
	}
	return b.Bytes()[:n]
}

var (
	jsVocab   = strings.Fields("function return const let var if else for while this new await async export import class extends document window addEventListener querySelector innerHTML = === => { } ( ) ; , . render state props useState effect fetch json")
	cssVocab  = strings.Fields(".card .nav .btn #app body { } margin: padding: display:flex color: #333 background: #fff border-radius: 4px font-size: 14px width: 100% @media (max-width:600px)")
	htmlVocab = strings.Fields("<div class=\"row\"> </div> <p> </p> <a href=\"/x\"> </a> <span> </span> <li> </li> <h2> </h2> Lorem ipsum dolor sit amet consectetur")
	jsonVocab = strings.Fields("\"id\": \"name\": \"price\": 12.5, 7, \"tags\": [\"a\",\"b\"], { }, \"stock\": true, false, null,")
)

// insertAt returns b with ins spliced in at off (everything after shifts).
func insertAt(b []byte, off int, ins string) []byte {
	out := append([]byte{}, b[:off]...)
	out = append(out, ins...)
	return append(out, b[off:]...)
}

func siteA() map[string][]byte {
	return map[string][]byte{
		"site/index.html":      wordSalad(1, 120<<10, htmlVocab, " "),
		"site/css/app.css":     wordSalad(2, 160<<10, cssVocab, " "),
		"site/js/app.v1.js":    wordSalad(3, 400<<10, jsVocab, " "),
		"site/js/vendor.js":    wordSalad(4, 600<<10, jsVocab, "\n"),
		"site/data/items.json": wordSalad(5, 300<<10, jsonVocab, " "),
		"site/img/hero.bin":    core.Data(6, 1536<<10),
		"site/legacy/old.css":  wordSalad(7, 80<<10, cssVocab, "\n"),
	}
}

// siteB is a developer's local edit of A: edits in the middle of three files,
// app.v1.js renamed to app.v2.js with an inserted block, one file dropped.
func siteB(a map[string][]byte) map[string][]byte {
	b := map[string][]byte{}
	for k, v := range a {
		b[k] = v
	}
	b["site/index.html"] = insertAt(a["site/index.html"], 60<<10, "<!-- build 2: new hero section -->")
	b["site/css/app.css"] = insertAt(a["site/css/app.css"], 90<<10, "\n.hero{padding:2rem}\n")
	b["site/data/items.json"] = insertAt(a["site/data/items.json"], 200<<10, `{"id":99999,"name":"new item"},`)
	b["site/js/app.v2.js"] = insertAt(a["site/js/app.v1.js"], 150<<10, "\nexport const BUILD = 2;\n")
	delete(b, "site/js/app.v1.js")
	delete(b, "site/legacy/old.css")
	return b
}

func contentType(k string) string {
	switch {
	case strings.HasSuffix(k, ".html"):
		return "text/html"
	case strings.HasSuffix(k, ".css"):
		return "text/css"
	case strings.HasSuffix(k, ".js"):
		return "text/javascript"
	case strings.HasSuffix(k, ".json"):
		return "application/json"
	}
	return "application/octet-stream"
}

func sortedKeys(m map[string][]byte) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// browser: the compressed-browser-storage workload on plain S3 plus optional
// ZeroS3 maintenance.
func browser() {
	const prof = "z2-consumer/browser"
	e := newEnv("browser")
	defer e.close()
	e.start()
	t := core.NewT(e.aws, e.cfg, "browser")
	must(e.aws.CreateBucket("web"))

	// 1. site A.
	a := siteA()
	for _, k := range sortedKeys(a) {
		put(t, e.aws, "web", k, a[k], contentType(k))
	}
	e.stop()
	e.cli(nil, "compact", "-store", e.store, "-pack-size-mib", "2", "-json")
	e.cli(nil, "tier", "move", "-store", e.store, "-from", "hot", "-to", "cold", "-all", "-apply", "-json")
	note(prof, "site A packed (adaptive compression) and moved to the cold tier", len(e.packIDs("cold")) > 0 && len(e.packIDs("hot")) == 0, "cold %d hot %d", len(e.packIDs("cold")), len(e.packIDs("hot")))
	afterA := e.stats()

	// 2. locally edited site B: only changed/new files are uploaded, like a sync tool.
	e.start()
	t = core.NewT(e.aws, e.cfg, "browser")
	b := siteB(a)
	var uploaded int64
	changed := 0
	for _, k := range sortedKeys(b) {
		if old, ok := a[k]; ok && bytes.Equal(old, b[k]) {
			continue
		}
		put(t, e.aws, "web", k, b[k], contentType(k))
		uploaded += int64(len(b[k]))
		changed++
	}
	e.stop()
	afterB := e.stats()
	newLoose := afterB.LooseChunkFileBytes - afterA.LooseChunkFileBytes
	note(prof, "CDC/CAS reuse: more than half of the uploaded bytes were already stored chunks",
		newLoose > 0 && newLoose < uploaded/2, "uploaded %d bytes in %d files, new chunk bytes %d (loose chunks %d)", uploaded, changed, newLoose, afterB.LooseChunkCount)
	note(prof, "ZeroS3 stats report deduplication", afterB.DedupAvoidedBytes > 0, "dedup_avoided_bytes=%d", afterB.DedupAvoidedBytes)

	// 3. compact the new chunks; old data stays cold.
	e.cli(nil, "compact", "-store", e.store, "-pack-size-mib", "2", "-json")
	note(prof, "A in cold and B-only data in hot (mixed tiers)", len(e.packIDs("cold")) > 0 && len(e.packIDs("hot")) > 0, "cold %d hot %d", len(e.packIDs("cold")), len(e.packIDs("hot")))

	// 4. ordinary reads of the visible site, then batch-delete the obsolete keys.
	e.start()
	t = core.NewT(e.aws, e.cfg, "browser")
	visible := map[string][]byte{}
	for k, v := range b {
		visible[k] = v
	}
	for _, c := range []core.Client{e.aws, e.mc} {
		for _, k := range sortedKeys(visible) {
			g, err := c.Get("web", k, core.GetOpts{})
			t.Check(c.Name()+" GET "+k+" byte-exact", err == nil && bytes.Equal(g.Body, visible[k]), "%v", err)
			mid := len(visible[k]) / 2
			rg, err := c.Get("web", k, core.GetOpts{Range: fmt.Sprintf("bytes=%d-%d", mid, mid+4095)})
			t.Check(c.Name()+" range "+k, err == nil && bytes.Equal(rg.Body, visible[k][mid:mid+4096]), "%v", err)
			_, err = c.Get("web", k, core.GetOpts{IfNoneMatch: g.ETag})
			t.Status(c.Name()+" conditional GET "+k, err, 304)
		}
	}
	hd, err := e.aws.Head("web", "site/index.html")
	t.Check("HEAD index.html reports the B size", err == nil && hd.Size == int64(len(b["site/index.html"])), "%+v %v", hd, err)
	obsolete := []string{"site/js/app.v1.js", "site/legacy/old.css", "site/never-existed.js"}
	deleted, failed, err := e.aws.DeleteMany("web", obsolete, false)
	t.Check("DeleteObjects removes the obsolete visible keys", err == nil && len(failed) == 0 && len(deleted) == len(obsolete), "%v %v %v", deleted, failed, err)
	for _, k := range obsolete[:2] {
		_, err := e.aws.Head("web", k)
		t.Status("obsolete "+k+" is no longer visible", err, 404)
	}
	pg, _ := e.aws.List("web", core.ListOpts{Prefix: "site/"})
	t.Check("listing shows exactly site B", strings.Join(pg.Keys, ",") == strings.Join(sortedKeys(b), ","), "%v", pg.Keys)
	r := t.Report()
	r.Profile = prof
	record(r)
	e.stop()

	// 5. history keeps A until pruned; physical bytes are untouched by deletion.
	var rows []struct {
		VersionID string `json:"version_id"`
		Deleted   bool   `json:"deleted"`
	}
	e.cli(&rows, "versions", "-store", e.store, "-bucket", "web", "-key", "site/js/app.v1.js", "-json")
	hasDeleted := false
	for _, r := range rows {
		hasDeleted = hasDeleted || r.Deleted
	}
	note(prof, "history retains the deleted app.v1.js", len(rows) >= 1 && hasDeleted, "%+v", rows)
	var hist []struct {
		VersionID string `json:"version_id"`
	}
	e.cli(&hist, "versions", "-store", e.store, "-bucket", "web", "-key", "site/index.html", "-json")
	note(prof, "history retains the previous index.html (site A)", len(hist) == 2, "%d rows", len(hist))
	beforePrune := e.stats()
	var gc0 gcJSON
	e.cli(&gc0, "gc", "-store", e.store, "-json")
	note(prof, "before pruning, gc finds nothing unreachable (A is still reachable through history)", gc0.ChunksUnreachable == 0, "%+v", gc0)

	// 6. explicit prune, then gc/repack reclaim the obsolete physical storage.
	var pr struct {
		VersionsPruned int `json:"versions_pruned"`
	}
	e.cli(&pr, "versions", "prune", "-store", e.store, "-bucket", "web", "-keep-last", "0", "-apply", "-json")
	note(prof, "prune retires the 5 historical versions (3 overwrites + 2 deletes)", pr.VersionsPruned == 5, "pruned %d", pr.VersionsPruned)
	var gc1 gcJSON
	e.cli(&gc1, "gc", "-store", e.store, "-apply", "-json")
	e.cli(nil, "repack", "-store", e.store, "-apply", "-max-live-percent", "100", "-pack-size-mib", "2", "-json")
	afterGC := e.stats()
	note(prof, "gc + repack reclaim obsolete physical storage", afterGC.ChunkStoreFileBytes < beforePrune.ChunkStoreFileBytes,
		"chunk store %d -> %d bytes (gc deleted %d bytes, %d packs)", beforePrune.ChunkStoreFileBytes, afterGC.ChunkStoreFileBytes, gc1.BytesDeleted, gc1.PacksDeleted)
	e.cli(nil, "verify", "-store", e.store, "-deep")

	// 7. the site is still byte-exact for ordinary clients after reclamation.
	e.start()
	t = core.NewT(e.aws, e.cfg, "browser-final")
	for _, k := range sortedKeys(b) {
		if k == "site/js/app.v1.js" || k == "site/legacy/old.css" {
			continue
		}
		g, err := e.aws.Get("web", k, core.GetOpts{})
		t.Check("after prune+gc+repack GET "+k, err == nil && bytes.Equal(g.Body, b[k]), "%v", err)
	}
	r = t.Report()
	r.Profile = prof + "/final"
	record(r)
}
