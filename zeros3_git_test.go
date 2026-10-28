package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repGitFixtures(t testing.TB) []repLabCodecFixture {
	t.Helper()
	raw, err := os.ReadFile("testing-harnesses/testdata/i1c-git/lab-git.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []repLabCodecFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func TestRepGitLabWireAndReaderAt(t *testing.T) {
	fixtures := repGitFixtures(t)
	if len(fixtures) != 8 {
		t.Fatalf("fixture count %d", len(fixtures))
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			// Pack bundles have no derived children, so their entire body is identical
			// after the pre-existing five-byte lab/production envelope difference.
			if !bytes.Equal(f.Root[5:], f.LabRoot[5:]) {
				t.Fatal("lab pack representation body changed")
			}
			src := newRepMem()
			for text, object := range f.Objects {
				want, err := decodeHexSHA256(text)
				if err != nil {
					t.Fatal(err)
				}
				if src.put(object) != want {
					t.Fatal("fixture component digest differs")
				}
			}
			root := src.put(f.Root)
			env := &repEnv{Src: src, Limits: repProdLimits}
			var out bytes.Buffer
			n, sum, err := env.reconstruct(root, &out)
			if err != nil || n != int64(len(f.Input)) || sum != sha256.Sum256(f.Input) || !bytes.Equal(out.Bytes(), f.Input) {
				t.Fatalf("exact pack replay: %v", err)
			}
			algo := repGTSHA1
			if strings.HasPrefix(f.Name, "sha256") {
				algo = repGTSHA256
			}
			p, err := repGTParsePackAt(bytes.NewReader(f.Input), int64(len(f.Input)), algo, repGTLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Resolve(); err != nil {
				t.Fatal(err)
			}
			if p.Version != binary.BigEndian.Uint32(f.Input[4:8]) {
				t.Fatal("pack version changed")
			}
			call, err := env.open(root)
			if err != nil {
				t.Fatal(err)
			}
			if call.Rep.Kind != repKindGit {
				t.Fatal("Git number changed")
			}
			index := call.Rep.Indexes[0]
			if index.KeyWidth != algo.Size() || int(index.Count) != len(p.Entries) {
				t.Fatal("native index shape differs")
			}
			for _, entry := range p.Entries {
				if _, found, err := call.resolve(entry.ID); err != nil || !found {
					t.Fatalf("native lookup failed: %v", err)
				}
			}
			reach, err := env.reachable(root)
			if err != nil {
				t.Fatal(err)
			}
			for id := range src.seen {
				if _, ok := reach.Role[id]; !ok {
					t.Fatalf("unprotected read %x", id)
				}
			}
		})
	}
}

func TestRepGitProductionPackAndIdx(t *testing.T) {
	for _, f := range repGitFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			l := newRepLab(t, repModePrefer)
			l.put("pack", f.Input)
			_, man := l.manifest("pack")
			root := repMustRoot(t, l.s, man.ManifestUUID)
			src := l.s.newRepSource(root.Descs, nil)
			call, err := (&repEnv{Src: src, Limits: repProdLimits}).open(root.Rep)
			src.close()
			if err != nil || call.Rep.Kind != repKindGit {
				t.Fatalf("Git analyzer: %v", err)
			}
			l.exact("production pack")
			l.offline(func() {
				if _, err := compactStore(l.dir, repSmallCompact()); err != nil {
					t.Fatal(err)
				}
			})
			l.exact("compacted pack")
		})
	}
	for _, algo := range []string{"sha1", "sha256"} {
		for _, variant := range []string{"v1", "ofs", "ref"} {
			t.Run(algo+"-"+variant+".idx", func(t *testing.T) {
				data, err := os.ReadFile("testing-harnesses/z3/gitz3/testdata/" + algo + "-" + variant + ".idx")
				if err != nil {
					t.Fatal(err)
				}
				l := newRepLab(t, repModePrefer)
				l.put("idx", data)
				_, man := l.manifest("idx")
				if !bytes.HasPrefix(data, []byte{0xff, 't', 'O', 'c'}) {
					if repHasRoot(l.s, man.ManifestUUID) || l.s.format.StoreFormatVersion >= 6 {
						t.Fatal("legacy idx v1 should remain a CDC fallback")
					}
					l.exact("idx v1 unsupported control")
					return
				}
				root := repMustRoot(t, l.s, man.ManifestUUID)
				src := l.s.newRepSource(root.Descs, nil)
				call, err := (&repEnv{Src: src, Limits: repProdLimits}).open(root.Rep)
				src.close()
				if err != nil || call.Rep.Kind != repKindConcat {
					t.Fatalf("closed idx representation: %v", err)
				}
				l.exact("idx v2 sections")
				if len(data) > 1200 {
					got := repReadAll(t, l.s, man, byteRange{1000, 1199})
					if !bytes.Equal(got, data[1000:1200]) {
						t.Fatal("idx range differs")
					}
				}
			})
		}
	}
}

func TestRepOCIRealRegistryComposition(t *testing.T) {
	dir := os.Getenv("ZEROS3_Z3_I1C_REAL_OCI")
	if dir == "" {
		t.Skip("set ZEROS3_Z3_I1C_REAL_OCI to the hash-verified registry cache")
	}
	type descriptor struct {
		Digest    string
		Size      int64
		MediaType string `json:"mediaType"`
		DiffID    string `json:"diff_id"`
		Plain     int64  `json:"plain_bytes"`
	}
	var images []struct {
		Tag              string
		Manifest, Config descriptor
		Layers           []descriptor
	}
	raw, err := os.ReadFile(filepath.Join(dir, "REAL_REGISTRY.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &images); err != nil {
		t.Fatal(err)
	}
	off, on := newRepLab(t, repModeOff), newRepLab(t, repModePrefer)
	for _, image := range images {
		for _, d := range append([]descriptor{image.Manifest, image.Config}, image.Layers...) {
			if !strings.HasPrefix(d.Digest, "sha256:") {
				t.Fatal("unsupported fixture digest")
			}
			key := strings.TrimPrefix(d.Digest, "sha256:")
			if _, err := decodeHexSHA256(key); err != nil {
				t.Fatal("invalid cached digest")
			}
			data, err := os.ReadFile(filepath.Join(dir, "blobs", key))
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(data)) != d.Size || fmt.Sprintf("sha256:%x", sha256.Sum256(data)) != d.Digest {
				t.Fatal("registry native identity mismatch")
			}
			off.put(key, data)
			on.put(key, data)
			if d.DiffID != "" {
				_, man := on.manifest(key)
				if repHasRoot(on.s, man.ManifestUUID) {
					root := repMustRoot(t, on.s, man.ManifestUUID)
					src := on.s.newRepSource(root.Descs, nil)
					call, err := (&repEnv{Src: src, Limits: repProdLimits}).open(root.Rep)
					src.close()
					if err != nil {
						t.Fatal(err)
					}
					if call.Rep.Kind != repKindGzip {
						t.Fatalf("known gzip layer kind=%d", call.Rep.Kind)
					}
					member := call.Rep.Members[len(call.Rep.Members)-1]
					if member.Size != d.Plain || fmt.Sprintf("sha256:%x", member.SHA) != d.DiffID {
						t.Fatal("production decoded child differs from config DiffID")
					}
					t.Logf("REAL busybox %s native=%s DiffID=%s root=Gzip/Concat", image.Tag, d.Digest, d.DiffID)
				} else {
					t.Logf("REAL busybox %s explicit CDC fallback: %v", image.Tag, on.s.repBuild(man))
				}
			}
		}
		a, b := repStoreBytes(t, off.s), repStoreBytes(t, on.s)
		t.Logf("REAL image=%s cumulative_CDC=%d cumulative_CDC_plus_rep=%d roots=%d", image.Tag, a.ChunkStoreFileBytes, b.ChunkStoreFileBytes, b.RepresentationRootCount)
	}
	on.exact("verified real registry bytes")
}

// Mechanically reused lab malformed-pack fixture constructors.
func repGitTestZlib(b []byte) []byte {
	var o bytes.Buffer
	w := zlib.NewWriter(&o)
	w.Write(b)
	w.Close()
	return o.Bytes()
}

func repGitTestEntry(kind uint8, payload []byte, base []byte) []byte {
	e := repGTencodeEntryHeader(nil, kind, int64(len(payload)))
	e = append(e, base...)
	return append(e, repGitTestZlib(payload)...)
}

func repGitTestPack(a repGTAlgo, count uint32, entries ...[]byte) []byte {
	b := []byte("PACK\x00\x00\x00\x02")
	b = binary.BigEndian.AppendUint32(b, count)
	for _, e := range entries {
		b = append(b, e...)
	}
	return append(b, a.Sum(b)...)
}

func repGitTestFixSum(a repGTAlgo, b []byte) {
	hs := a.Size()
	copy(b[len(b)-hs:], a.Sum(b[:len(b)-hs]))
}

func TestRepGitHostileFallback(t *testing.T) {
	a := repGTSHA1
	delta := []byte{1, 1, 1, 'x'}
	missing := bytes.Repeat([]byte{0xaa}, 20)
	thin := repGitTestPack(a, 1, repGitTestEntry(7, delta, missing))
	cycle := repGitTestPack(a, 2, repGitTestEntry(7, delta, missing), repGitTestEntry(7, delta, bytes.Repeat([]byte{0xbb}, 20)))
	huge := repGTencodeEntryHeader(nil, 3, 200<<20)
	huge = append(huge, repGitTestZlib([]byte("x"))...)
	cases := map[string][]byte{
		"thin": thin, "unresolved-cycle": cycle,
		"huge-object": repGitTestPack(a, 1, huge),
		"count-limit": repGitTestPack(a, 1<<31),
		"truncated":   []byte("PACK\x00\x00\x00\x02"),
	}
	bad := append([]byte(nil), repGitFixtures(t)[0].Input...)
	bad[len(bad)-1] ^= 1
	cases["checksum"] = bad
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			l := newRepLab(t, repModePrefer)
			l.put("hostile", data)
			_, man := l.manifest("hostile")
			if repHasRoot(l.s, man.ManifestUUID) {
				t.Fatal("hostile pack received optional root")
			}
			l.exact("hostile Git CDC fallback")
		})
	}
}

func TestRepGitRealReexports(t *testing.T) {
	dir := os.Getenv("ZEROS3_Z3_I1C_REAL_GIT")
	if dir == "" {
		t.Skip("set ZEROS3_Z3_I1C_REAL_GIT to cached public Git reexports")
	}
	off, on := newRepLab(t, repModeOff), newRepLab(t, repModePrefer)
	var previous map[string]bool
	for _, name := range []string{"hiredis-full.pack", "hiredis-reordered.pack", "hiredis-delta.pack", "hiredis-full.idx", "hiredis-reordered.idx", "hiredis-delta.idx"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		off.put(name, data)
		on.put(name, data)
		_, man := on.manifest(name)
		if !repHasRoot(on.s, man.ManifestUUID) {
			t.Fatalf("%s fallback: %v", name, on.s.repBuild(man))
		}
		current := repPhysicalOf(t, on.s, man.ManifestUUID)
		shared := 0
		for id := range current {
			if previous[id] {
				shared++
			}
		}
		a, b := repStoreBytes(t, off.s), repStoreBytes(t, on.s)
		t.Logf("REAL_REEXPORT %s SHA256=%x size=%d cumulative_CDC=%d cumulative_CDC_plus_rep=%d components=%d shared_previous=%d", name, sha256.Sum256(data), len(data), a.ChunkStoreFileBytes, b.ChunkStoreFileBytes, len(current), shared)
		previous = current
	}
	on.exact("public Git reexports")
}

func FuzzRepGitDecode(f *testing.F) {
	raw, err := os.ReadFile("testing-harnesses/testdata/i1c-git/lab-git.json")
	if err != nil {
		f.Fatal(err)
	}
	var fixtures []repLabCodecFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		f.Fatal(err)
	}
	for i, x := range fixtures {
		f.Add(x.Root, uint8(i))
	}
	f.Fuzz(func(t *testing.T, data []byte, pick uint8) {
		if len(data) > 1<<20 {
			return
		}
		fixture := fixtures[int(pick)%len(fixtures)]
		src := newRepMem()
		for _, object := range fixture.Objects {
			src.put(object)
		}
		root := src.put(data)
		env := &repEnv{Src: src, Limits: repLimits{MaxDepth: 4, MaxMembers: 4096, MaxRepBytes: 1 << 20, MaxWhole: 2 << 20, MaxObjects: 4096}}
		env.reconstruct(root, io.Discard)
		env.reachable(root)
	})
}

func FuzzRepGitReaderAt(f *testing.F) {
	for _, x := range repGitFixtures(f) {
		f.Add(x.Input)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		lim := repGTLimits{MaxObjects: 4096, MaxObjectSize: 1 << 20, MaxInflated: 2 << 20, MaxDeltaDepth: 16, MaxDeltaOut: 2 << 20, MaxPack: 1 << 20}
		for _, a := range []repGTAlgo{repGTSHA1, repGTSHA256} {
			p, err := repGTParsePackAt(bytes.NewReader(data), int64(len(data)), a, lim)
			if err == nil {
				p.Resolve()
			}
			repGTParseIdxAt(bytes.NewReader(data), int64(len(data)), a, lim)
		}
	})
}

func TestRepGitRecipeBoundaries(t *testing.T) {
	f := repGitFixtures(t)[0]
	src := newRepMem()
	for _, object := range f.Objects {
		src.put(object)
	}
	root := src.put(f.Root)
	call, err := (&repEnv{Src: src, Limits: repProdLimits}).open(root)
	if err != nil {
		t.Fatal(err)
	}
	meta, pieces, err := gitTable(call)
	if err != nil {
		t.Fatal(err)
	}
	get := func(id [32]byte) ([]byte, error) { return append([]byte(nil), src.objs[id]...), nil }
	for _, cap := range []int64{0, 12, 20, int64(len(f.Input) - 1)} {
		if err := repGitRebuildSeq(meta, pieces, get, &repCapWriter{max: cap}); err == nil {
			t.Fatalf("write failure at cap %d ignored", cap)
		}
	}
	huge := []byte{repGTmetaMagic, byte(repGTSHA1)}
	huge = binary.AppendUvarint(huge, 2)
	huge = binary.AppendUvarint(huge, 1<<32)
	if err := repGitRebuildSeq(huge, pieces, get, io.Discard); err == nil {
		t.Fatal("oversized entry count accepted")
	}
	extra := append(append([]gitEntry(nil), pieces...), pieces[0])
	if err := repGitRebuildSeq(meta, extra, get, io.Discard); err == nil {
		t.Fatal("unused pieces accepted")
	}
}
