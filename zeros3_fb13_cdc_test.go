package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type fb13Series struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
}

func fb13Cut(raw []byte, target int) int {
	if target == cdcTargetChunkSize {
		return findCDCBoundary(raw, true)
	}
	raw = raw[:min(len(raw), target*4)]
	start := target/4 - cdcMaskLookback
	if start >= len(raw) {
		return len(raw)
	}
	var h uint64
	for ; start < target/4-1 && start < len(raw); start++ {
		h = (h << 1) + gearTable[raw[start]]
	}
	pos, h := cdcScan(raw, start, min(target-1, len(raw)), h, uint64(target-1))
	if pos >= 0 {
		return pos + 1
	}
	if pos, _ = cdcScan(raw, -pos-1, len(raw), h, uint64(target/2-1)); pos >= 0 {
		return pos + 1
	}
	return len(raw)
}

type fb13Chunk struct {
	sum [32]byte
	raw []byte
}

func fb13Chunks(raw []byte, target int) []fb13Chunk {
	var chunks []fb13Chunk
	for len(raw) > 0 {
		n := fb13Cut(raw, target)
		chunks = append(chunks, fb13Chunk{sha256.Sum256(raw[:n]), raw[:n]})
		raw = raw[n:]
	}
	return chunks
}

// Publish the alternate boundaries only to disposable harness stores. Production
// readers should reconstruct by manifest refs, regardless of those boundaries.
func fb13PutChunks(t *testing.T, l *authLab, key string, raw []byte, chunks []fb13Chunk) {
	t.Helper()
	ing := ingestResult{size: int64(len(raw)), objSHA256: sha256.Sum256(raw), etagMD5: md5.Sum(raw)}
	batch := l.s.newCASBatch()
	defer batch.Abort()
	for _, c := range chunks {
		_, err := batch.add(c.sum, c.raw)
		mustOK(t, err)
		ing.chunks = append(ing.chunks, chunkRef{SHA256: hex.EncodeToString(c.sum[:]), Length: int64(len(c.raw))})
	}
	mustOK(t, batch.Flush())
	_, err := l.s.commitIngested("b", key, ing, "application/octet-stream", nil, putCondition{})
	mustOK(t, err)
	l.want[key] = raw
}

func fb13Allocation(t *testing.T, root string) (total, packs, manifest int64) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		n := info.Sys().(*syscall.Stat_t).Blocks * 512
		total += n
		if filepath.Ext(path) == ".pack" {
			packs += n
		}
		if filepath.Base(filepath.Dir(path)) == "manifests" {
			manifest += n
		}
		return nil
	})
	mustOK(t, err)
	return
}

func fb13CPU() time.Duration {
	var r syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &r) != nil {
		return 0
	}
	return time.Duration(r.Utime.Nano() + r.Stime.Nano())
}

func fb13LocatorBytes(level *locIndex) int64 {
	n := cap(level.recs)*int(unsafe.Sizeof(locRec{})) + cap(level.dups)*int(unsafe.Sizeof(locRec{})) + cap(level.tbl)*4
	if level.filter != nil {
		n += cap(level.filter.blocks) * 32
	}
	return int64(n)
}

func TestFB13NativeChunkEconomics(t *testing.T) {
	p := os.Getenv("ZEROS3_FB13_SERIES")
	if p == "" {
		t.Skip("set ZEROS3_FB13_SERIES to the fixture manifest")
	}
	data, err := os.ReadFile(p)
	mustOK(t, err)
	var input []fb13Series
	mustOK(t, json.Unmarshal(data, &input))
	for _, family := range input {
		t.Run(family.Name, func(t *testing.T) {
			var versions [][]byte
			for i, path := range family.Paths {
				raw, err := os.ReadFile(path)
				mustOK(t, err)
				t.Logf("version=%d logical=%d sha256=%x", i, len(raw), sha256.Sum256(raw))
				versions = append(versions, raw)
			}
			for _, lane := range []struct {
				name string
				size int
				mix  bool
			}{{"v1-64", 64 << 10, false}, {"gear-32", 32 << 10, false},
				{"gear-16", 16 << 10, false}, {"v1-to-16", 16 << 10, true}} {
				if lane.mix && len(versions) < 2 {
					continue
				}
				l := newAuthLab(t)
				l.s.writeBinaryManifests = true
				known := map[[32]byte]bool{}
				var metaBytes, refs, logical, repeat, crossV1, cpuNS, probeCPU int64
				coldStart := fb13CPU()
				for i, raw := range versions {
					target := lane.size
					if i == 0 && lane.mix {
						target = cdcTargetChunkSize
					}
					cpu := fb13CPU()
					chunks := fb13Chunks(raw, target)
					cpuNS += int64(fb13CPU() - cpu)
					var v1 map[[32]byte]bool
					if target != cdcTargetChunkSize {
						start := fb13CPU()
						v1 = map[[32]byte]bool{}
						for _, c := range fb13Chunks(raw, cdcTargetChunkSize) {
							v1[c.sum] = true
						}
						probeCPU += int64(fb13CPU() - start)
					}
					logical += int64(len(raw))
					for _, c := range chunks {
						refs++
						if known[c.sum] {
							repeat += int64(len(c.raw))
						}
						if v1[c.sum] {
							crossV1 += int64(len(c.raw))
						}
					}
					for _, c := range chunks {
						known[c.sum] = true
					}
					key := strconv.Itoa(i)
					fb13PutChunks(t, l, key, raw, chunks)
					bin, err := encodeBinaryManifest(l.man(key))
					mustOK(t, err)
					metaBytes += int64(len(bin))
				}
				ingestCPU := fb13CPU() - coldStart - time.Duration(probeCPU)
				reoptStart := fb13CPU()
				l.reclaim()
				reoptCPU := fb13CPU() - reoptStart
				l.exact(lane.name)
				l.verifyOK(lane.name)
				ps := l.s.packSnap()
				var locatorBytes int64
				for _, level := range ps.levels {
					locatorBytes += fb13LocatorBytes(level)
				}
				rows := slices.SortedFunc(maps.Keys(known), func(a, b [32]byte) int { return bytes.Compare(a[:], b[:]) })
				lookupStart := time.Now()
				for range 16 {
					for _, sha := range rows {
						if _, ok := ps.lookup(sha); !ok {
							t.Fatalf("missing packed digest %x", sha)
						}
					}
				}
				lookupNS := time.Since(lookupStart).Nanoseconds() / int64(16*max(1, len(rows)))
				last := l.man(strconv.Itoa(len(versions) - 1))
				lastBody := versions[len(versions)-1]
				readLen := min(len(lastBody), 4096)
				var readBytes int64
				readStart := time.Now()
				for i := range 32 {
					off := int64(i*142857) % int64(len(lastBody)-readLen+1)
					var at int64
					for _, c := range last.Chunks {
						if at < off+int64(readLen) && at+c.Length > off {
							readBytes += c.Length
						}
						at += c.Length
					}
					if got := l.read(strconv.Itoa(len(versions)-1), byteRange{off, off + int64(readLen) - 1}); !bytes.Equal(got, lastBody[off:off+int64(readLen)]) {
						t.Fatal("range mismatch")
					}
				}
				rangeNS := time.Since(readStart).Nanoseconds() / 32
				l.close()
				total, packs, mans := fb13Allocation(t, l.dir)
				t.Logf("lane=%s logical=%d references=%d unique=%d retained=%d packs=%d manifest_allocated=%d manifest_encoded=%d cross_version_reuse=%d cross_boundary_reuse=%d locator_bytes=%d lookup_ns=%d range_decode_bytes=%d range_ns=%d chunk_cpu=%s ingest_cpu=%s repack_cpu=%s",
					lane.name, logical, refs, len(known), total, packs, mans, metaBytes, repeat, crossV1, locatorBytes, lookupNS,
					readBytes/32, rangeNS, time.Duration(cpuNS), ingestCPU, reoptCPU)
			}
		})
	}
}

func TestFB13FrozenCDCControl(t *testing.T) {
	raw := bytes.Repeat([]byte("cdc-version-control"), 1<<15)
	cdc := newCDCChunker(bytes.NewReader(raw))
	defer cdc.release()
	for _, row := range fb13Chunks(raw, cdcTargetChunkSize) {
		got, err := cdc.nextView()
		mustOK(t, err)
		if !bytes.Equal(got, row.raw) {
			t.Fatal("research baseline differs from streaming CDC")
		}
	}
	_, err := cdc.nextView()
	if err != io.EOF {
		t.Fatalf("streaming CDC remainder: %v", err)
	}
}
