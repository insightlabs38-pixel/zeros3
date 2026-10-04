// Command z2_consumer is the ZeroS3-specific consumer-contract harness: the
// storage-state lifecycle that sits beside the portable profile/conformance
// suite.
//
//	invariance  one ordinary S3 read scenario over loose, packed raw, packed
//	            compressed, warm, cold and mixed-tier stores must show clients
//	            byte-identical observations; the AWS SDK also runs the full
//	            Core Client Profile on loose and mixed-tier stores
//	browser     site A -> locally edited site B -> compact/tier/batch-delete/
//	            history prune/gc/repack, reads byte-exact throughout
//	artifact    multipart checkpoint, range read, localized-edit delta sync,
//	            snapshot, tier move
//	rebalance   content-aware placement (tier policy + rebalance) over the browser
//	            workload and a checkpoint projection: hottest reference wins, reads
//	            unchanged, policy/prune change placement; prints measurements
//
// It starts the real zeros3 binary (ZEROS3_BIN) and talks to it only over S3
// and the documented CLI.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"zeros3-testing/profile/core"
)

var bin, onlyStates string

type env struct {
	store string
	srv   *core.Server
	cfg   core.Config
	aws   core.Client
	mc    core.Client
}

func newEnv(tag string) *env {
	dir, err := os.MkdirTemp("", "zeros3-consumer-"+tag+"-")
	must(err)
	return &env{store: dir, cfg: core.Config{AccessKey: core.DefaultAccessKey, SecretKey: core.DefaultSecretKey, Region: "us-east-1"}}
}

func (e *env) start() {
	var err error
	e.srv, err = core.StartServer(bin, e.store, e.cfg.Region)
	must(err)
	e.cfg.Endpoint = e.srv.Endpoint
	e.aws, err = core.NewAWS(e.cfg)
	must(err)
	e.mc, err = core.NewMinio(e.cfg)
	must(err)
}

func (e *env) stop() {
	if e.srv != nil {
		e.srv.Stop()
		e.srv = nil
	}
}

func (e *env) close() { e.stop(); os.RemoveAll(e.store) }

// cli runs a zeros3 subcommand. Maintenance commands need the server stopped.
func (e *env) cli(out any, args ...string) {
	var so, se bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		must(fmt.Errorf("zeros3 %s: %v\n%s", strings.Join(args, " "), err, se.String()))
	}
	if out != nil {
		must(json.Unmarshal(so.Bytes(), out))
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z2_consumer:", err)
		os.Exit(2)
	}
}

type statsJSON struct {
	LogicalCurrentBytes        int64 `json:"logical_current_bytes"`
	LogicalChunkReferenceBytes int64 `json:"logical_chunk_reference_bytes"`
	UniqueReachableChunkBytes  int64 `json:"unique_reachable_chunk_bytes"`
	ChunkStoreFileBytes        int64 `json:"chunk_store_file_bytes"`
	LooseChunkFileBytes        int64 `json:"loose_chunk_file_bytes"`
	LooseChunkCount            int   `json:"loose_chunk_count"`
	DedupAvoidedBytes          int64 `json:"dedup_avoided_bytes"`
}

type gcJSON struct {
	ChunksUnreachable int   `json:"chunks_unreachable"`
	PackedDeadBytes   int64 `json:"packed_dead_bytes"`
	ChunksDeleted     int   `json:"chunks_deleted"`
	PacksDeleted      int   `json:"packs_deleted"`
	BytesDeleted      int64 `json:"bytes_deleted"`
}

func (e *env) stats(extra ...string) statsJSON {
	var s statsJSON
	e.cli(&s, append([]string{"stats", "-store", e.store, "-json"}, extra...)...)
	return s
}

func (e *env) packIDs(tier string) []string {
	dir := filepath.Join(e.store, "packs")
	if tier != "hot" {
		dir = filepath.Join(e.store, "tiers", tier, "packs")
	}
	ents, _ := os.ReadDir(dir)
	var ids []string
	for _, f := range ents {
		if id, ok := strings.CutSuffix(f.Name(), ".pack"); ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func put(t *core.T, c core.Client, b, k string, body []byte, ct string) {
	if _, err := c.Put(b, k, body, core.PutOpts{ContentType: ct, Meta: map[string]string{"origin": "z2-consumer"}}); err != nil {
		t.Check("PutObject "+k, false, "%v", err)
		must(err)
	}
}

// observe checks every object the way an ordinary consumer would and returns
// a digest of everything a client can see (never LastModified or ids). Equal
// digests across physical states are the invariance claim.
func observe(t *core.T, cs []core.Client, bucket string, want map[string][]byte) string {
	h := sha256.New()
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, c := range cs {
		for _, k := range keys {
			body := want[k]
			n := len(body)
			g, err := c.Get(bucket, k, core.GetOpts{})
			t.Check(c.Name()+" full GET "+k, err == nil && bytes.Equal(g.Body, body), "err %v len %d want %d", err, len(g.Body), n)
			hd, err := c.Head(bucket, k)
			t.Check(c.Name()+" HEAD "+k, err == nil && hd.Size == int64(n) && hd.ETag == g.ETag && hd.Meta["origin"] == "z2-consumer", "%+v %v", hd, err)
			for _, r := range [][2]int{{0, min(99, n-1)}, {n / 3, min(n-1, n/3+200_000)}, {max(n-777, 0), n - 1}} {
				rg, err := c.Get(bucket, k, core.GetOpts{Range: fmt.Sprintf("bytes=%d-%d", r[0], r[1])})
				t.Check(fmt.Sprintf("%s range %s %d-%d", c.Name(), k, r[0], r[1]), err == nil && bytes.Equal(rg.Body, body[r[0]:r[1]+1]), "err %v", err)
				if c == cs[0] && !strings.HasPrefix(k, "late/") {
					fmt.Fprintf(h, "range %s %d-%d %x %s\n", k, r[0], r[1], sha256.Sum256(rg.Body), rg.ContentRange)
				}
			}
			_, err = c.Get(bucket, k, core.GetOpts{IfNoneMatch: g.ETag})
			t.Status(c.Name()+" conditional GET "+k+" is 304", err, 304)
			_, err = c.Get(bucket, k, core.GetOpts{IfMatch: "0000"})
			t.Status(c.Name()+" If-Match mismatch "+k+" is 412", err, 412)
			if c == cs[0] && !strings.HasPrefix(k, "late/") {
				fmt.Fprintf(h, "obj %s %d %s %s %x\n", k, hd.Size, hd.ETag, hd.ContentType, sha256.Sum256(g.Body))
			}
		}
	}
	pg, err := cs[0].List(bucket, core.ListOpts{})
	t.Check("list shows exactly the dataset", err == nil && strings.Join(pg.Keys, ",") == strings.Join(keys, ","), "%v %v", pg.Keys, err)
	for _, k := range pg.Keys {
		if !strings.HasPrefix(k, "late/") { // the mixed-tier fixture's extra loose upload is checked above, not part of the baseline digest
			fmt.Fprintf(h, "list %s\n", k)
		}
	}
	// CopyObject reads the stored chunks server-side.
	k0 := keys[0]
	_, err = cs[0].Copy(bucket, "copied/"+k0, bucket, k0, nil)
	cp, gerr := cs[0].Get(bucket, "copied/"+k0, core.GetOpts{})
	t.Check("CopyObject from stored data is byte-exact", err == nil && gerr == nil && bytes.Equal(cp.Body, want[k0]), "%v %v", err, gerr)
	_ = cs[0].Delete(bucket, "copied/"+k0)
	if u, err := cs[0].PresignGet(bucket, k0, 60e9); err == nil {
		code, b := t.Fetch("GET", u, nil, nil)
		t.Check("presigned GET is byte-exact", code == 200 && bytes.Equal(b, want[k0]), "status %d", code)
	}
	return hex.EncodeToString(h.Sum(nil))
}

type result struct {
	Profile string        `json:"profile"`
	Passed  int           `json:"passed"`
	Failed  int           `json:"failed"`
	Skipped int           `json:"skipped"`
	Reports []core.Report `json:"reports"`
}

var res = result{Profile: core.Profile}

func record(r core.Report) {
	res.Reports = append(res.Reports, r)
	res.Passed, res.Failed, res.Skipped = res.Passed+r.Passed, res.Failed+r.Failed, res.Skipped+r.Skipped
}

func main() {
	scen := flag.String("scenario", "invariance,browser,artifact", "comma-separated: invariance, browser, artifact, rebalance")
	flag.StringVar(&onlyStates, "states", "", "invariance only: comma-separated physical states to run (default all; the first run state is the baseline)")
	asJSON := flag.Bool("json", false, "emit the JSON summary only")
	flag.Parse()
	bin = os.Getenv("ZEROS3_BIN")
	if bin == "" {
		fmt.Fprintln(os.Stderr, "z2_consumer: ZEROS3_BIN is required")
		os.Exit(2)
	}
	for _, kv := range os.Environ() { // ambient AWS_* must not override fixed credentials
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "AWS_") {
			os.Unsetenv(k)
		}
	}
	for _, s := range strings.Split(*scen, ",") {
		switch s {
		case "invariance":
			invariance()
		case "browser":
			browser()
		case "artifact":
			artifact()
		case "rebalance":
			rebalance()
		default:
			must(fmt.Errorf("unknown scenario %q", s))
		}
	}
	if *asJSON {
		out, _ := json.MarshalIndent(res, "", " ")
		fmt.Println(string(out))
	} else {
		for _, r := range res.Reports {
			fmt.Printf("%-22s %-14s passed=%d failed=%d skipped=%d\n", r.Profile, r.Client, r.Passed, r.Failed, r.Skipped)
			for _, f := range r.Failures {
				fmt.Printf("  FAIL %s: %s: %s\n", f.Scenario, f.Contract, f.Detail)
			}
		}
	}
	if res.Failed > 0 {
		os.Exit(1)
	}
}

// note records one environment-level check that belongs to no single client.
func note(profile, contract string, ok bool, format string, args ...any) {
	r := core.Report{Profile: profile, Client: "-", Endpoint: "-"}
	if ok {
		r.Passed = 1
	} else {
		r.Failed, r.Failures = 1, []core.Failure{{Scenario: profile, Contract: contract, Detail: fmt.Sprintf(format, args...)}}
	}
	record(r)
}
