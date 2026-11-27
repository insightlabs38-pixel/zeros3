package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Publish only in a disposable experiment. The normal namespace still uses
// the original immutable manifest, so HEAD/list metadata is untouched.
func gf2PublishNative(t *testing.T, s *Store, baseMan, targetMan manifestV1, base, target []byte) (*authRecord, error) {
	t.Helper()
	var members []repMember
	var at int64
	for _, src := range baseMan.Chunks {
		id, err := decodeHexSHA256(src.SHA256)
		if err != nil || src.Length < 1 || src.Length > int64(len(base))-at ||
			sha256.Sum256(base[at:at+src.Length]) != id {
			t.Fatal("wrong independently provided original base")
		}
		members = append(members, repMember{Role: repStored, SHA: id, Size: src.Length})
		at += src.Length
	}
	if at != int64(len(base)) {
		t.Fatal("base manifest does not cover original")
	}
	cfg := gf2MatchConfig{"relative-4", 8, 4, 8, true, true}
	_, encoded, err := gf2PrepareNativePatch(members, base, target, cfg)
	if err != nil {
		return nil, err
	}
	return gf2CommitNativePatch(s, targetMan, target, encoded)
}

type gf2NativePatch struct {
	programs [][]byte
	record   []byte
}

func (p gf2NativePatch) ownBytes() int64 {
	total := packPriceLen(p.record) + packRecordBytes
	for _, prog := range p.programs {
		total += packPriceLen(prog) + packRecordBytes
	}
	return total
}

func gf2PrepareNativePatch(sources []repMember, base, target []byte, cfg gf2MatchConfig) (int64, gf2NativePatch, error) {
	var out gf2NativePatch
	rep := repObject{Kind: repKindBitPatch, Size: int64(len(target)), Members: slices.Clone(sources)}
	rep.Recipe = binary.AppendUvarint(nil, uint64(len(sources)))
	index := gf2IndexSource(base, cfg)
	var copied int64
	for off := 0; off < len(target); off += gf2FrameSize {
		raw := target[off:min(off+gf2FrameSize, len(target))]
		compiled, stats := gf2FrameProgram(base, raw, index, cfg)
		copied += stats.copied
		out.programs = append(out.programs, compiled)
		id := sha256.Sum256(compiled)
		rep.Members = append(rep.Members, repMember{Role: repStored, SHA: id, Size: int64(len(compiled))})
		sum := sha256.Sum256(raw)
		rep.Recipe = append(rep.Recipe, sum[:]...)
	}
	if _, err := parseBitPatchLayout(&rep); err != nil {
		return 0, gf2NativePatch{}, err
	}
	var err error
	out.record, err = rep.marshal()
	return copied, out, err
}

func gf2CommitNativePatch(s *Store, targetMan manifestV1, target []byte, patch gf2NativePatch) (*authRecord, error) {
	sum := sha256.Sum256(target)
	if targetMan.ObjectSHA256 != fmt.Sprintf("%x", sum[:]) || targetMan.TotalLength != int64(len(target)) {
		return nil, errors.New("target manifest differs")
	}
	b := s.newCASBatch()
	defer b.Abort()
	for _, prog := range patch.programs {
		if _, err := b.Add(prog); err != nil {
			return nil, err
		}
	}
	id, err := b.Add(patch.record)
	if err != nil {
		return nil, err
	}
	if err := b.Flush(); err != nil {
		return nil, err
	}
	rec := &authRecord{SHA: sum, Size: int64(len(target)), Gen: 1, Form: authFormRep,
		Root: &repRoot{Rep: id, ObjSHA: sum, ObjSize: int64(len(target)), ManifestUUID: targetMan.ManifestUUID}}
	if _, _, err := s.authProve(rec, 8); err != nil {
		return nil, err
	}
	return rec, s.authPublish(rec)
}

func gf2SignedReader(t *testing.T, l *authLab) func(string, int, int) []byte {
	t.Helper()
	srv, signer := newTestServerAndSignerFor(t, l.s)
	server := httptest.NewServer(srv)
	t.Cleanup(server.Close)
	return func(key string, start, count int) []byte {
		t.Helper()
		headers := map[string]string{}
		want := http.StatusOK
		if count > 0 {
			headers["Range"] = fmt.Sprintf("bytes=%d-%d", start, start+count-1)
			want = http.StatusPartialContent
		}
		resp := doSignedRequest(t, server.Client(), server.URL, signer, "GET", "/b/"+key, nil, headers)
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		mustOK(t, err)
		if resp.StatusCode != want {
			t.Fatalf("signed status %d expected %d: %s", resp.StatusCode, want, raw)
		}
		return raw
	}
}

func TestGF2NativeAuthority(t *testing.T) {
	l := newAuthLab(t)
	// The exact same bytes moved by an integer number of bits generate
	// both compressed literal and shifted COPY instructions across chunks.
	base := make([]byte, 384<<10)
	for i := 0; i < len(base); i += 32 {
		block := sha256.Sum256(binary.LittleEndian.AppendUint64(nil, uint64(i)))
		copy(base[i:], block[:])
	}
	raw := make([]byte, len(base)-1)
	for i := range raw {
		raw[i] = base[i]>>5 | base[i+1]<<3
	}
	copy(raw[32761:], []byte("distinct cross-frame literal"))
	l.put("base", base)
	l.put("target", raw)
	mustOK(t, l.s.ensureFeature(agFeature))
	_, err := gf2PublishNative(t, l.s, l.man("base"), l.man("target"), base, raw)
	mustOK(t, err)
	ctx := context.Background()
	_, err = l.s.finishAuthorityArchive(ctx, 0)
	mustOK(t, err)
	l.close()
	l.open()
	if !slices.Contains(l.s.formatSnapshot().Features, bitPatchFeature) {
		t.Fatal("missing format admission bit")
	}
	if old := os.Getenv("ZEROS3_GF2_OLD_BINARY"); old != "" {
		l.close()
		out, stderr, status := runZeros3CLI(t, old, "verify", "-store", l.dir, "-deep")
		if status == 0 || !strings.Contains(out+stderr, bitPatchFeature) {
			t.Fatalf("older reader did not refuse the experimental feature: %d %s %s", status, out, stderr)
		}
		l.open()
	}
	l.exact("authority frame reopen")
	l.verifyOK("authority frame reopen")
	mustOK(t, l.s.DeleteObject("b", "base"))
	delete(l.want, "base")
	_, err = l.s.finishAuthorityArchive(ctx, 0)
	mustOK(t, err)
	l.exact("source manifest pruned")
	l.verifyOK("source manifest pruned")
	signed := gf2SignedReader(t, l)
	if got := signed("target", gf2FrameSize-9, 4096); !bytes.Equal(got, raw[gf2FrameSize-9:gf2FrameSize-9+4096]) {
		t.Fatal("signed frame range changed")
	}
	if got := signed("target", 0, 0); !bytes.Equal(got, raw) {
		t.Fatal("signed frame whole changed")
	}
	sum := sha256.Sum256(raw)
	peer := l.s.AuthorityCaps()
	peer.Features = slices.DeleteFunc(peer.Features, func(f string) bool { return f == bitPatchFeature })
	var wire bytes.Buffer
	if err := l.s.ExportAuthorityClosureFor(sum, peer, nil, &wire); !errors.Is(err, errAuthTransferUnsupported) || wire.Len() != 0 {
		t.Fatalf("negotiated an incompatible peer: %v, %d bytes", err, wire.Len())
	}
	mustOK(t, l.s.ExportAuthorityClosureFor(sum, l.s.AuthorityCaps(), nil, &wire))
	dst := newAuthLab(t)
	if err := dst.s.ImportAuthorityClosure(bytes.NewReader(wire.Bytes()[:wire.Len()-1])); err == nil {
		t.Fatal("accepted a truncated authority transfer")
	}
	noRecord, err := dst.s.authLoad(sum)
	mustOK(t, err)
	if noRecord != nil {
		t.Fatal("truncated transfer published a record")
	}
	mustOK(t, dst.s.ImportAuthorityClosure(bytes.NewReader(wire.Bytes())))
	dst.put("target", raw)
	_, err = dst.s.finishAuthorityArchive(ctx, 0)
	mustOK(t, err)
	dst.exact("verified native transfer")
	dst.verifyOK("verified native transfer")
	entries, err := l.s.captureSnapshotEntries("b", "")
	mustOK(t, err)
	snap := snapshotDescriptorV1{
		SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(),
		CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries,
	}
	mustOK(t, l.s.publishSnapshot(snap))
	var bundle bytes.Buffer
	_, err = l.s.exportBundle(snap.SnapshotID, &bundle, true)
	mustOK(t, err)
	ordinary := newAuthLab(t)
	_, err = ordinary.s.importBundle(bytes.NewReader(bundle.Bytes()))
	mustOK(t, err)
	imported, err := ordinary.s.readSnapshot(snap.SnapshotID)
	mustOK(t, err)
	if len(imported.Entries) != 1 || imported.Entries[0].Key != "target" {
		t.Fatal("bundle lost the target descriptor")
	}
	entry := imported.Entries[0]
	manifestDigest, err := decodeHexSHA256(entry.ManifestSHA256)
	mustOK(t, err)
	manifest, err := ordinary.s.readVerifiedManifest(entry.ManifestUUID, manifestDigest)
	mustOK(t, err)
	if got := repReadAll(t, ordinary.s, manifest, byteRange{0, manifest.TotalLength - 1}); !bytes.Equal(got, raw) {
		t.Fatal("restored snapshot bytes differ")
	}
	ordinary.verifyOK("ordinary snapshot bundle")
	mustOK(t, l.s.deleteSnapshot(snap.SnapshotID))
	before := l.s.formatSnapshot().Features
	if !slices.Contains(before, bitPatchFeature) {
		t.Fatal("feature was dropped while a dependent representation was live")
	}
	_, err = l.s.AuthorityRevertAll()
	mustOK(t, err)
	if slices.Contains(l.s.formatSnapshot().Features, bitPatchFeature) {
		t.Fatal("feature still present after revert")
	}
	l.close()
	l.open()
	l.exact("ordinary CDC after lowering authority floor")
	l.verifyOK("ordinary CDC after lowering authority floor")
	if old := os.Getenv("ZEROS3_GF2_OLD_BINARY"); old != "" {
		l.close()
		out, stderr, status := runZeros3CLI(t, old, "verify", "-store", l.dir, "-deep")
		if status != 0 {
			t.Fatalf("old binary could not verify downgrade: %d %s %s", status, out, stderr)
		}
	}
}

func TestGF2NativeAuthorityPanel(t *testing.T) {
	root, families := gf0Corpus(t)
	for _, fam := range families {
		if fam.Name != "typescript-gzip" {
			continue
		}
		base, _ := gf0ReadFixture(t, root, fam, 0)
		target, _ := gf0ReadFixture(t, root, fam, 1)
		for round := range 2 {
			lanes := []string{"cdc", "archive", "bitpatch"}
			if round != 0 {
				slices.Reverse(lanes)
			}
			for _, lane := range lanes {
				t.Run(fmt.Sprintf("%d/%s", round, lane), func(t *testing.T) {
					l := newAuthLab(t)
					l.s.writeBinaryManifests = true
					l.put("base", base)
					l.put("target", target)
					ctx := context.Background()
					_, err := l.s.finishAuthorityArchive(ctx, 0)
					mustOK(t, err)
					before, cpu := time.Now(), fb13CPU()
					if lane != "cdc" {
						mustOK(t, l.s.ensureFeature(agFeature))
					}
					switch lane {
					case "archive":
						_, err = l.s.AuthorityOptimize(ctx, OptimizePolicy{
							AuthPolicy: AuthPolicy{Apply: true, Order: "oldest", AllowDelta: true, AllowRep: true, CodecReplay: true, HardCodecs: true, MaxDepth: 3},
							Profile:    "archive", Canonical: true, Executables: true, Splice: true, SparseFiles: true, FastReencode: true,
						})
					case "bitpatch":
						_, err = gf2PublishNative(t, l.s, l.man("base"), l.man("target"), base, target)
					}
					mustOK(t, err)
					planWall, planCPU := time.Since(before).Nanoseconds(), int64(fb13CPU()-cpu)
					_, err = l.s.finishAuthorityArchive(ctx, 0)
					mustOK(t, err)
					l.close()
					total, pack, _ := fb13Allocation(t, l.dir)
					l.open()
					get := gf2SignedReader(t, l)
					var whole, small []int64
					for i := range 8 {
						before = time.Now()
						got := get("target", 0, 0)
						whole = append(whole, time.Since(before).Nanoseconds())
						if !bytes.Equal(got, target) {
							t.Fatal("signed whole differs")
						}
						for k := range 6 {
							at := (len(target) - 4096) * (i*6 + k) / 47
							before = time.Now()
							got := get("target", at, 4096)
							small = append(small, time.Since(before).Nanoseconds())
							if !bytes.Equal(got, target[at:at+4096]) {
								t.Fatal("signed range differs")
							}
						}
					}
					l.verifyOK("signed physical lanes")
					row, err := json.Marshal(map[string]any{"lane": lane, "round": round, "allocated": total, "pack_allocated": pack,
						"plan_cpu_ns": planCPU, "plan_wall_ns": planWall, "target_whole_p50_p95_p99_ns": gf0Percentiles(whole),
						"target_range_p50_p95_p99_ns": gf0Percentiles(small)})
					mustOK(t, err)
					fmt.Printf("GF2AUTH %s\n", row)
				})
			}
		}
	}
}

func gf2CrashChild(t *testing.T, dir string) {
	s, err := OpenStore(dir)
	mustOK(t, err)
	defer s.Close()
	l := &authLab{t: t, s: s, dir: dir}
	base, target := l.man("base"), l.man("target")
	b := l.read("base", byteRange{0, base.TotalLength - 1})
	v := l.read("target", byteRange{0, target.TotalLength - 1})
	_, err = gf2PublishNative(t, s, base, target, b, v)
	mustOK(t, err)
}

func TestGF2NativeCrashBoundaries(t *testing.T) {
	root := newAuthLab(t)
	base := genRandomBytes(721, 256<<10)
	target := slices.Clone(base)
	copy(target[49132:], []byte("replacement across exact publication"))
	root.put("base", base)
	root.put("target", target)
	mustOK(t, root.s.ensureFeature(agFeature))
	root.close()
	for _, point := range []string{hookCASAfterFlush, hookAuthAfterFormat, hookAuthPublished} {
		t.Run(point, func(t *testing.T) {
			dst := t.TempDir()
			mustOK(t, os.CopyFS(dst, os.DirFS(root.dir)))
			if err := os.Remove(filepath.Join(dst, "LOCK")); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if !runKilled(t, "gf2-bitpatch", dst, point, 1) {
				t.Fatal("cut was not reached")
			}
			l := &authLab{t: t, dir: dst, want: root.want}
			l.open()
			defer l.close()
			l.exact("atomic cut recovery")
			l.verifyOK("atomic cut recovery")
			existing, err := l.s.authLoad(sha256.Sum256(target))
			mustOK(t, err)
			if (existing != nil) != (point == hookAuthPublished) {
				t.Fatalf("unexpected commit boundary at %s", point)
			}
			if existing == nil {
				_, err = gf2PublishNative(t, l.s, l.man("base"), l.man("target"), base, target)
				mustOK(t, err)
			}
			_, err = l.s.finishAuthorityArchive(context.Background(), 0)
			mustOK(t, err)
			l.exact("replayed interrupted candidate")
			l.verifyOK("replayed interrupted candidate")
		})
	}
}
