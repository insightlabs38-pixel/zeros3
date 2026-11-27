package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type fb14Posting struct {
	keys [4]uint64
	rows [4]uint32
	next uint8
}

type fb14Row struct {
	sum     [32]byte
	raw     []byte
	plain   []byte
	codec   byte
	base    int
	encoded []byte
}

type fb14Lab struct {
	rows      []fb14Row
	digests   map[[32]byte]int
	versions  [][]int
	postings  []fb14Posting
	matcher   int64
	probes    int64
	reuses    int64
	comp      *packCompressor
	patchComp *packCompressor
}

// Sample rolling hashes and retain one low hash per independent range
// of the transformed feature space. A posting nominates a base only.
func fb14Features(raw []byte) [4]uint64 {
	features := [4]uint64{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}
	var h uint64
	for i, b := range raw {
		h = (h << 1) + gearTable[b]
		if i < 32 || h&7 != 0 {
			continue
		}
		x := xdMix(h)
		slot := x >> 62
		features[slot] = min(features[slot], x)
	}
	return features
}

func (l *fb14Lab) similar(raw []byte, position int, features [4]uint64) []int {
	votes := make(map[int]int, 20)
	for _, f := range features {
		if f == ^uint64(0) {
			continue
		}
		p := l.postings[f&uint64(len(l.postings)-1)]
		for i, row := range p.rows {
			if row != 0 && f == p.keys[i] {
				votes[int(row-1)] += 4
			}
		}
	}
	if len(l.versions) > 0 {
		prior := l.versions[len(l.versions)-1]
		for i := max(0, position-1); i < min(len(prior), position+2); i++ {
			votes[prior[i]] += 1
		}
	}
	candidates := make([]int, 0, len(votes))
	for id := range votes {
		row := l.rows[id]
		if row.base == -1 && len(row.raw)*2 >= len(raw) && len(row.raw) <= 2*len(raw) {
			candidates = append(candidates, id)
		}
	}
	slices.SortFunc(candidates, func(a, b int) int {
		if votes[a] != votes[b] {
			return votes[b] - votes[a]
		}
		return b - a
	})
	return candidates[:min(4, len(candidates))]
}

func (l *fb14Lab) ingest(raw []byte, target int) error {
	var version []int
	for _, c := range fb13Chunks(raw, target) {
		if id, exists := l.digests[c.sum]; exists {
			l.reuses++
			version = append(version, id)
			continue
		}
		plain, codec := l.comp.encode(c.raw)
		r := fb14Row{sum: c.sum, raw: c.raw, base: -1, plain: bytes.Clone(plain), codec: codec}
		id := len(l.rows)
		if len(l.postings) > 0 && len(plain) > 80 {
			features := fb14Features(c.raw)
			for _, candidate := range l.similar(c.raw, len(version), features) {
				base := l.rows[candidate]
				p := dwDcoreDefaultParams()
				p.MaxAnchors = 1 << 15
				index, err := dwDcoreBuildIndex([][]byte{base.raw}, p)
				if err != nil {
					return err
				}
				l.probes++
				l.matcher = max(l.matcher, int64(len(index.table))*8)
				encoded := fb14Patch(base.sum, index.Match(c.raw), l.patchComp)
				if len(encoded)+max(24, len(plain)/10) >= len(plain) ||
					r.base >= 0 && len(encoded) >= len(r.encoded) {
					continue
				}
				got, err := fb14Decode(c.sum, len(c.raw), encoded, base.raw)
				if err != nil || !bytes.Equal(got, c.raw) {
					return errors.Join(err, errAuthMismatch)
				}
				r.base, r.encoded = candidate, encoded
			}
			if r.base == -1 {
				for _, f := range features {
					if f == ^uint64(0) {
						continue
					}
					p := &l.postings[f&uint64(len(l.postings)-1)]
					p.keys[p.next], p.rows[p.next] = f, uint32(id+1)
					p.next = (p.next + 1) % 4
				}
			}
		}
		l.digests[r.sum] = id
		l.rows = append(l.rows, r)
		version = append(version, id)
	}
	l.versions = append(l.versions, version)
	return nil
}

type fb14Retention struct {
	roots  []bool
	deltas []bool
	pins   []bool
}

func (l *fb14Lab) retention(from int, rewrite bool) fb14Retention {
	r := fb14Retention{make([]bool, len(l.rows)), make([]bool, len(l.rows)), make([]bool, len(l.rows))}
	for _, ver := range l.versions[from:] {
		for _, row := range ver {
			r.roots[row] = true
		}
	}
	gain := make([]int, len(l.rows))
	for id, row := range l.rows {
		if r.roots[id] && row.base >= 0 {
			gain[row.base] += len(row.plain) - len(row.encoded)
		}
	}
	for id, row := range l.rows {
		if !r.roots[id] || row.base < 0 {
			continue
		}
		// Depth one makes the cost of each pin independent. Quote the
		// entire base record and refuse a base with a negative group gain.
		if rewrite && !r.roots[row.base] && gain[row.base] <= len(l.rows[row.base].plain)+packRecordBytes {
			continue
		}
		r.deltas[id] = true
		if !r.roots[row.base] {
			r.pins[row.base] = true
		}
	}
	return r
}

func (l *fb14Lab) physical(t *testing.T, store *Store, r fb14Retention, from int, versions [][]byte) {
	t.Helper()
	p, err := store.newPackBuilder(tierHot, nil, len(l.rows))
	mustOK(t, err)
	defer p.Abort()
	var dep, rewriteBytes int64
	var rowIDs []int
	var deltas int
	for id, row := range l.rows {
		if !r.roots[id] && !r.pins[id] {
			continue
		}
		rowIDs = append(rowIDs, id)
		switch {
		case r.deltas[id]:
			deltas++
			p.addEncoded(row.sum, len(row.raw), row.encoded, fb14DeltaCodec)
		default:
			if r.pins[id] {
				dep += int64(len(row.plain)) + packRecordBytes
			}
			if r.roots[id] && row.base >= 0 {
				rewriteBytes += int64(len(row.plain)) + packRecordBytes
			}
			p.addEncoded(row.sum, len(row.raw), row.plain, row.codec)
		}
	}

	// Charge real binary manifests in the same sealed file, along with
	// their index bytes. Use the original geometry, with no CDC2 marker.
	var manifestBytes int64
	for ver := from; ver < len(l.versions); ver++ {
		var refs []chunkRef
		for _, id := range l.versions[ver] {
			row := l.rows[id]
			refs = append(refs, chunkRef{Length: int64(len(row.raw)), SHA256: hex.EncodeToString(row.sum[:])})
		}
		raw := versions[ver]
		m := buildManifestV1FromRefs(refs, int64(len(raw)), sha256.Sum256(raw), "", "application/octet-stream", nil)
		meta, err := encodeBinaryManifest(m)
		mustOK(t, err)
		coded, codec := l.comp.encode(meta)
		p.addEncoded(sha256.Sum256(meta), len(meta), coded, codec)
		manifestBytes += int64(len(coded)) + packRecordBytes
	}

	path, entries, err := p.Finish()
	mustOK(t, err)
	defer os.Remove(path)
	f, err := os.Open(path)
	mustOK(t, err)
	defer f.Close()
	fi, err := f.Stat()
	mustOK(t, err)
	if len(entries) != len(rowIDs)+len(l.versions)-from {
		t.Fatal("pack index count disagrees with records")
	}
	identity, err := readPackIndexIdentity(f, fi.Size())
	mustOK(t, err)
	if identity.count != uint64(len(entries)) {
		t.Fatal("sealed footer count differs")
	}
	h := sha256.New()
	_, err = io.Copy(h, io.NewSectionReader(f, 0, fi.Size()-packFooterSize))
	mustOK(t, err)
	if [32]byte(h.Sum(nil)) != identity.bodySHA {
		t.Fatal("sealed body digest differs")
	}
	index := make([]byte, identity.length)
	_, err = f.ReadAt(index, int64(identity.offset))
	mustOK(t, err)
	if crc32.Checksum(index, packCRC) != identity.indexCRC {
		t.Fatal("sealed index checksum differs")
	}
	want := make([]byte, packIndexEntrySize)
	for i, e := range entries {
		putPackIndexEntry(want, e)
		if !bytes.Equal(want, index[i*packIndexEntrySize:(i+1)*packIndexEntrySize]) {
			t.Fatal("sealed index entry differs")
		}
	}
	byID := make(map[int]packEntry, len(rowIDs))
	for pos, id := range rowIDs {
		byID[id] = entries[pos]
	}
	get := func(e packEntry) []byte {
		payload := make([]byte, e.stored)
		_, err := f.ReadAt(payload, int64(e.off))
		mustOK(t, err)
		return payload
	}
	var decode func(int) (raw []byte, read int64)
	decode = func(id int) ([]byte, int64) {
		e, ok := byID[id]
		if !ok {
			t.Fatalf("missing retained row %d", id)
		}
		payload := get(e)
		if e.codec != fb14DeltaCodec {
			data, err := decodePackPayload(e.codec, payload, e.logical, e.sha, nil)
			mustOK(t, err)
			return data, int64(e.logical)
		}
		base, n := decode(l.rows[id].base)
		data, err := fb14Decode(e.sha, int(e.logical), payload, base)
		mustOK(t, err)
		return data, n + int64(e.logical)
	}
	for ver := from; ver < len(l.versions); ver++ {
		manifest := entries[len(rowIDs)+ver-from]
		b, err := decodePackPayload(manifest.codec, get(manifest), manifest.logical, manifest.sha, nil)
		mustOK(t, err)
		decoded, err := decodeBinaryManifest(b)
		mustOK(t, err)
		h := sha256.New()
		for i, id := range l.versions[ver] {
			body, _ := decode(id)
			if hex.EncodeToString(l.rows[id].sum[:]) != decoded.Chunks[i].SHA256 || !bytes.Equal(body, l.rows[id].raw) {
				t.Fatal("chunk or its reference changed after reopen")
			}
			_, _ = h.Write(body)
		}
		if hex.EncodeToString(h.Sum(nil)) != decoded.ObjectSHA256 || sha256.Sum256(versions[ver]) != [32]byte(h.Sum(nil)) {
			t.Fatal("materialized version digest mismatch")
		}
	}
	for _, id := range rowIDs {
		if r.pins[id] {
			_, _ = decode(id)
		}
	}
	var reads, times []int64
	last := l.versions[len(l.versions)-1]
	ends := make([]int64, len(last)+1)
	for i, id := range last {
		ends[i+1] = ends[i] + int64(len(l.rows[id].raw))
	}
	ver := versions[len(versions)-1]
	for i := range 48 {
		// Range starts are identical to the native baseline. Find the
		// intersected records as production locator requests would.
		n := min(4096, len(ver))
		start := int64(i*196973) % int64(max(1, len(ver)-n+1))
		end := start + int64(n)
		var got []byte
		var read int64
		begin := time.Now()
		i, _ := slices.BinarySearch(ends[1:], start+1)
		for ; i < len(last) && ends[i] < end; i++ {
			buf, fetched := decode(last[i])
			at, next := ends[i], ends[i+1]
			got = append(got, buf[max(0, start-at):min(next-at, end-at)]...)
			read += fetched
		}
		if !bytes.Equal(got, ver[start:end]) {
			t.Fatal("disk range mismatch")
		}
		reads = append(reads, read)
		times = append(times, time.Since(begin).Nanoseconds())
	}
	allocated := fi.Sys().(*syscall.Stat_t).Blocks * 512
	t.Logf("retained_versions=%d allocated=%d pack_apparent=%d manifest_extent=%d dependency_extents=%d rebuild_extents=%d rows=%d delta=%d p95_decode_bytes=%d p95_range_ns=%d",
		len(l.versions)-from, allocated, fi.Size(), manifestBytes, dep, rewriteBytes, len(rowIDs), deltas, fb14Quantile(reads, 95), fb14Quantile(times, 95))
}

func TestFB14ChunkDeltaPanel(t *testing.T) {
	for _, family := range fb14Input(t) {
		t.Run(family.Name, func(t *testing.T) {
			versions := fb14Read(t, family)
			for _, target := range []int{8 << 10, 16 << 10, 32 << 10, 64 << 10} {
				for _, delta := range []bool{false, true} {
					l := &fb14Lab{digests: make(map[[32]byte]int), comp: newCompressor(true), patchComp: newCompressor(true)}
					if delta {
						l.postings = make([]fb14Posting, 2048)
					}
					cpu := fb13CPU()
					for _, v := range versions {
						mustOK(t, l.ingest(v, target))
					}
					t.Logf("target=%d chunk_delta=%t ingest_cpu=%s sketch_allocated=%d matcher_peak=%d candidates=%d dedup_hits=%d",
						target, delta, fb13CPU()-cpu, len(l.postings)*int(unsafe.Sizeof(fb14Posting{})), l.matcher, l.probes, l.reuses)
					scratch := newAuthLab(t)

					l.physical(t, scratch.s, l.retention(0, false), 0, versions)
					if from := len(versions) - 5; from > 0 {
						t.Log("keep-last-4 ingest representations")
						l.physical(t, scratch.s, l.retention(from, false), from, versions)
						if delta {
							t.Log("keep-last-4 retention repricing")
							l.physical(t, scratch.s, l.retention(from, true), from, versions)
						}
					}
				}
			}
		})
	}
}

func FuzzFB14ChunkDeltaRecord(f *testing.F) {
	base := bytes.Repeat([]byte("nearest source revision"), 64)
	mutated := bytes.Clone(base)
	copy(mutated[300:], []byte("replacement"))
	idx, err := dwDcoreBuildIndex([][]byte{base}, dwDcoreDefaultParams())
	if err != nil {
		f.Fatal(err)
	}
	patch := fb14Patch(sha256.Sum256(base), idx.Match(mutated), newCompressor(true))
	f.Add(patch, uint16(len(mutated)))
	f.Fuzz(func(t *testing.T, p []byte, size uint16) {
		n := int(size) % 4096
		sum := sha256.Sum256(mutated)
		out, err := fb14Decode(sum, n, p, base)
		if err == nil && (sha256.Sum256(out) != sum || len(out) != n) {
			t.Fatal("accepted incorrect decoded identity")
		}
	})
}
