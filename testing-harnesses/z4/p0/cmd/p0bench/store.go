package main

// mkstore builds a persistent, deterministic store through the real server for
// maintenance/scale series; verifystore replays it in a fresh process;
// startup measures open/recovery latency.

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"time"
)

type manEnt struct {
	Bucket, Key string
	C           Content
	SHA         string
	Deleted     bool
}

func cmdMkstore(args []string) {
	fs := flag.NewFlagSet("mkstore", flag.ExitOnError)
	c := addCommon(fs)
	set := addSet(fs)
	dir := fs.String("dir", "", "store directory to create (required)")
	man := fs.String("manifest", "", "manifest JSONL path (default DIR.manifest.jsonl)")
	delfrac := fs.Float64("delfrac", 0, "fraction of keys deleted after the last revision")
	loadMode := fs.String("load", "single", "single | multipart")
	fs.Parse(args)
	set.resolve("")
	if *dir == "" {
		die("mkstore: -dir required")
	}
	if *man == "" {
		*man = *dir + ".manifest.jsonl"
	}
	os.RemoveAll(*dir)
	os.MkdirAll(*dir, 0o755)
	srv, cl := func() (*Server, *S3) {
		s, err := StartServer(c.binary(), *dir, c.profDir(0), c.srvArgs(), c.envList())
		if err != nil {
			die("%v", err)
		}
		cl := &S3{c: newClient(c.conc + 8), base: s.URL}
		if err := waitReady(cl.c, s.URL); err != nil {
			die("%v", err)
		}
		return s, cl
	}()
	cl.CreateBucket(set.bucket)
	t0 := time.Now()
	var last []*Obj
	for rev := 0; rev < max(1, set.revs); rev++ {
		last = set.gen(rev, 256<<20)
		if err := populate(cl, last, PutOpt{Mode: *loadMode, PartSize: 8 << 20, Payload: "unsigned"}, c.conc); err != nil {
			die("mkstore populate rev %d: %v", rev, err)
		}
	}
	populateS := time.Since(t0).Seconds()
	deleted := map[int]bool{}
	if *delfrac > 0 {
		r := rand.New(rand.NewPCG(set.seed, 4242))
		for i := range last {
			if r.Float64() < *delfrac {
				deleted[i] = true
			}
		}
		err := pool(len(last), c.conc, func(i int) error {
			if !deleted[i] {
				return nil
			}
			req, _ := http.NewRequest("DELETE", cl.base+"/"+last[i].Bucket+"/"+last[i].Key, nil)
			sign(req, "UNSIGNED-PAYLOAD")
			return cl.finish(req)
		})
		if err != nil {
			die("mkstore delete: %v", err)
		}
	}
	srv.Stop()
	f, _ := os.Create(*man)
	w := bufio.NewWriter(f)
	for i, o := range last {
		b, _ := json.Marshal(manEnt{o.Bucket, o.Key, o.C, o.SHA, deleted[i]})
		w.Write(append(b, '\n'))
	}
	w.Flush()
	f.Close()
	sz := DuStore(*dir)
	emit(c, map[string]any{"scenario": "mkstore", "dir": *dir, "objs": set.objs, "revs": set.revs, "delfrac": *delfrac, "populate_s": populateS,
		"logical_bytes_latest": totalBytes(last), "store": sz, "params": map[string]any{"size": set.size, "sizes": set.sizes, "kind": set.kind, "editfrac": set.editfrac, "conc": c.conc}})
}

func loadManifest(path string) []*manEnt {
	f, err := os.Open(path)
	if err != nil {
		die("%v", err)
	}
	defer f.Close()
	var out []*manEnt
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var m manEnt
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			die("bad manifest line")
		}
		out = append(out, &m)
	}
	return out
}

func cmdVerifyStore(args []string) {
	fs := flag.NewFlagSet("verifystore", flag.ExitOnError)
	c := addCommon(fs)
	dir := fs.String("dir", "", "store")
	man := fs.String("manifest", "", "manifest")
	fs.Parse(args)
	ents := loadManifest(*man)
	srv, cl := func() (*Server, *S3) {
		s, err := StartServer(c.bin, *dir, "", c.srvArgs(), c.envList())
		if err != nil {
			die("%v", err)
		}
		cl := &S3{c: newClient(c.conc + 8), base: s.URL}
		if err := waitReady(cl.c, s.URL); err != nil {
			die("%v", err)
		}
		return s, cl
	}()
	var objs []*Obj
	gone := 0
	for _, e := range ents {
		if e.Deleted {
			gone++
			continue
		}
		objs = append(objs, &Obj{Bucket: e.Bucket, Key: e.Key, C: e.C, SHA: e.SHA})
	}
	t := time.Now()
	bad, err := VerifyAll(cl, objs, max(c.conc, 4))
	// deleted keys must stay deleted
	for _, e := range ents {
		if !e.Deleted {
			continue
		}
		req, _ := http.NewRequest("HEAD", cl.base+"/"+e.Bucket+"/"+e.Key, nil)
		sign(req, "UNSIGNED-PAYLOAD")
		r, err2 := cl.do(req)
		if err2 == nil {
			r.Body.Close()
			if r.StatusCode != 404 {
				bad++
			}
		}
	}
	srv.Stop()
	ok := err == nil && bad == 0
	emit(c, map[string]any{"scenario": "verifystore", "dir": *dir, "live": len(objs), "deleted": gone, "bad": bad, "err": fmt.Sprint(err), "verified": ok, "wall_s": time.Since(t).Seconds()})
	if !ok {
		os.Exit(1)
	}
}

// startup: time from exec to first successful ListBuckets (open + recovery).
func cmdStartup(args []string) {
	fs := flag.NewFlagSet("startup", flag.ExitOnError)
	c := addCommon(fs)
	dir := fs.String("dir", "", "store")
	fs.Parse(args)
	for rep := 0; rep < c.reps; rep++ {
		if c.cache == "cold" {
			dropCaches()
		}
		d0 := readDisk(diskDev)
		t := time.Now()
		srv, err := StartServer(c.binary(), *dir, c.profDir(rep), c.srvArgs(), c.envList())
		if err != nil {
			die("%v", err)
		}
		cl := &S3{c: newClient(4), base: srv.URL}
		err = waitReady(cl.c, srv.URL)
		ready := time.Since(t).Seconds()
		p := readProc(srv.PID())
		d1 := readDisk(diskDev)
		srv.Stop()
		emit(c, map[string]any{"scenario": "startup", "rep": rep, "dir": *dir, "ready_s": ready, "ok": err == nil, "cache": c.cache,
			"rss_mib": float64(p.HWMKB) / 1024, "rchar": p.RChar, "dev_read_bytes": (d1.SectorsR - d0.SectorsR) * 512, "cpu_s": p.UserS + p.SysS, "server_lifetime": rusageMap(srv)})
	}
}
