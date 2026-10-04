package main

import (
	"fmt"
	"strings"

	"zeros3-testing/profile/core"
)

func dataset() map[string][]byte {
	half := append(core.Data(3, 1<<20), make([]byte, 1<<20)...)
	return map[string][]byte{
		"text/repeat.txt": bytesRepeat("The quick brown fox jumps over the lazy dog.\n", 2<<20),
		"bin/random.bin":  core.Data(2, 3<<20),
		"mixed/half.bin":  half,
		"tiny":            core.Data(4, 1234),
		"mp/big.bin":      nil, // filled by multipart upload below
	}
}

func bytesRepeat(s string, n int) []byte {
	out := make([]byte, 0, n)
	for len(out) < n {
		out = append(out, s...)
	}
	return out[:n]
}

type physState struct {
	name      string
	transform func(e *env, want map[string][]byte)
}

func compact(args ...string) func(*env, map[string][]byte) {
	return func(e *env, _ map[string][]byte) {
		e.cli(nil, append([]string{"compact", "-store", e.store, "-pack-size-mib", "2", "-json"}, args...)...)
	}
}

// mixed leaves loose, hot, warm and cold data in one store.
func mixed(e *env, want map[string][]byte) {
	e.cli(nil, "compact", "-store", e.store, "-pack-size-mib", "1", "-json")
	ids := e.packIDs("hot")
	if len(ids) < 4 {
		must(fmt.Errorf("mixed-tier fixture needs >=4 packs, got %d", len(ids)))
	}
	e.cli(nil, "tier", "move", "-store", e.store, "-from", "hot", "-to", "warm", "-pack", ids[0], "-apply", "-json")
	e.cli(nil, "tier", "move", "-store", e.store, "-from", "hot", "-to", "cold", "-pack", ids[1], "-pack", ids[2], "-apply", "-json")
	e.start() // a late upload stays loose beside the packs
	t := core.NewT(e.aws, e.cfg, "mixed-late-upload")
	late := core.Data(9, 700_000)
	put(t, e.aws, "inv", "late/loose.bin", late, "application/octet-stream")
	want["late/loose.bin"] = late
	e.stop()
}

// invariance runs one consumer read scenario over every physical state and
// requires identical client-visible observations.
func invariance() {
	states := []physState{
		{"loose-hot", nil},
		{"packed-raw-hot", compact("-compression", "off")},
		{"packed-compressed-hot", compact()},
		{"packed-warm", compact("-tier", "warm")},
		{"packed-cold", compact("-tier", "cold")},
		{"mixed-tier", mixed},
	}
	digests := map[string]string{}
	chunkBytes := map[string]int64{}
	var first string
	for _, st := range states {
		if onlyStates != "" && !strings.Contains(","+onlyStates+",", ","+st.name+",") {
			continue
		}
		e := newEnv(st.name)
		e.start()
		t := core.NewT(e.aws, e.cfg, "invariance/"+st.name)
		must(e.aws.CreateBucket("inv"))
		want := dataset()
		for k, b := range want {
			if b != nil {
				put(t, e.aws, "inv", k, b, "application/octet-stream")
			}
		}
		// A multipart-created object (11 MiB: 5+5+1) is part of every state.
		big := core.Data(5, 11<<20)
		id, err := e.aws.MPCreate("inv", "mp/big.bin", core.PutOpts{Meta: map[string]string{"origin": "z2-consumer"}})
		must(err)
		var parts []core.Part
		for i, off := range []int{0, 5 << 20, 10 << 20} {
			end := min(off+5<<20, len(big))
			etag, err := e.aws.MPPart("inv", "mp/big.bin", id, i+1, big[off:end])
			must(err)
			parts = append(parts, core.Part{N: i + 1, ETag: etag})
		}
		_, err = e.aws.MPComplete("inv", "mp/big.bin", id, parts)
		must(err)
		want["mp/big.bin"] = big
		e.stop()

		if st.transform != nil {
			st.transform(e, want)
		}
		chunkBytes[st.name] = e.stats().ChunkStoreFileBytes
		e.cli(nil, "verify", "-store", e.store, "-deep")
		e.start()

		clients := []core.Client{e.aws}
		if st.name == "packed-cold" || st.name == "mixed-tier" {
			clients = append(clients, e.mc) // minio-go reads the same stored bytes
		}
		digests[st.name] = observe(t, clients, "inv", want)
		if first == "" {
			first = digests[st.name]
		}
		t.Check(st.name+": client-visible observations equal the loose-hot baseline", digests[st.name] == first, "digest %s vs %s", digests[st.name], first)
		r := t.Report()
		r.Profile = "z2-consumer/invariance/" + st.name
		record(r)

		// Complete Core Client Profile (AWS SDK) on loose and mixed-tier stores.
		if st.name == "loose-hot" || st.name == "mixed-tier" {
			r := core.Run(e.aws, e.cfg)
			r.Profile = "z2-consumer/core-profile/" + st.name
			record(r)
		}
		e.close()
	}
	if chunkBytes["packed-raw-hot"] == 0 || chunkBytes["packed-compressed-hot"] == 0 {
		return // a restricted -states run
	}
	note("z2-consumer/invariance", "compressed packs are smaller than raw packs (the states really differ physically)",
		chunkBytes["packed-compressed-hot"] < chunkBytes["packed-raw-hot"], "compressed %d raw %d", chunkBytes["packed-compressed-hot"], chunkBytes["packed-raw-hot"])
}
