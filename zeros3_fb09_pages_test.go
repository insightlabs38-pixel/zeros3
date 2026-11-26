package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFB09MetadataPagesMatchCheckpoint(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	mustOK(t, err)
	defer s.Close()
	secret := sha256.Sum256([]byte("FB09 checkpoint fixture"))
	mustOK(t, os.WriteFile(filepath.Join(dir, "journal", "metadata-page-key"), secret[:], 0600))
	s.writeMetadataPages = true
	mustOK(t, s.CreateBucket("b"))
	for i := range 80 {
		_, err := s.PutObject("b", fmt.Sprintf("object/%03d", i), []byte("metadata-page fixture"), "text/plain", nil)
		mustOK(t, err)
	}
	mustOK(t, s.nsConsolidate(true))
	before, err := s.verifyMetadataPages()
	mustOK(t, err)
	if before.Rows != 80 || before.Pages < 2 {
		t.Fatalf("unexpected metadata page tree: %+v", before)
	}
	path := filepath.Join(dir, "journal", metadataArchiveName)
	fb07FlipPackByte(t, path, int64(len(metadataArchiveMagic)+4+metaPageHeader))
	if _, err := s.verifyMetadataPages(); err == nil {
		t.Fatal("accepted a damaged metadata page")
	}
	verified, err := s.Verify(true)
	mustOK(t, err)
	if !verified.OK() {
		t.Fatalf("derived page damage affected authoritative verification: %+v", verified)
	}
	s.writeMetadataPages = false
	_, err = s.PutObject("b", "after", []byte("next checkpoint"), "text/plain", nil)
	mustOK(t, err)
	mustOK(t, s.nsConsolidate(true))
	if _, err := s.verifyMetadataPages(); err == nil {
		t.Fatal("accepted the previous checkpoint's metadata pages")
	}
	s.writeMetadataPages = true
	mustOK(t, s.nsConsolidate(true))
	after, err := s.verifyMetadataPages()
	mustOK(t, err)
	if after.Rows != 81 || after.Root == before.Root {
		t.Fatalf("new checkpoint did not establish its own tree: %+v", after)
	}
	t.Logf("exact checkpoint comparison checked %d then %d visible rows; rejected damaged and stale sidecars", before.Rows, after.Rows)
}

func TestFB09MetadataPageCanonicalOrderAndLimit(t *testing.T) {
	secret := sha256.Sum256([]byte("fixed local test boundary key"))
	value := &objectEntry{manifestUUID: fb09Manifest().ManifestUUID, manifestSHA256: sha256.Sum256([]byte("manifest")),
		size: 200, etag: "tag", contentType: "application/octet-stream", seq: 7}
	makeKey := func(i int) string { return fmt.Sprintf("b\x00adversarial/%08d", i) }
	selector := newMetadataTree(secret, nil)
	var inputs []string
	for i := 0; len(inputs) < 2400; i++ {
		key := makeKey(i)
		if !selector.cut([]byte(key), metaPageTarget) {
			inputs = append(inputs, key)
		}
	}
	var expected [32]byte
	maximum := 0
	for seed := range 5 {
		order := rand.New(rand.NewSource(int64(seed))).Perm(len(inputs))
		var keys []string
		for _, i := range order {
			keys = append(keys, inputs[i])
		}
		slices.Sort(keys)
		pages := map[[32]byte][]byte{}
		b := newMetadataTree(secret, func(sum [32]byte, data []byte) error {
			maximum = max(maximum, len(data))
			pages[sum] = data
			return nil
		})
		for _, key := range keys {
			mustOK(t, b.add([]byte(key), value))
		}
		root, err := b.finish()
		mustOK(t, err)
		if seed == 0 {
			expected = root.sum
		} else if root.sum != expected {
			t.Fatal("insertion order changed the canonical root")
		}
		i := 0
		n, visited, err := walkMetadataTree(root.sum, func(sum [32]byte) ([]byte, error) {
			return pages[sum], nil
		}, func(key []byte, stored *objectEntry) error {
			if i >= len(keys) || string(key) != keys[i] || *stored != *value {
				t.Fatalf("tree walk differs at row %d", i)
			}
			i++
			return nil
		})
		mustOK(t, err)
		if n != uint64(len(inputs)) || visited != len(pages) {
			t.Fatalf("incomplete page reachability: %d entries, %d pages", n, visited)
		}
	}
	if maximum > metaPageLimit || maximum < metaPageTarget*8 {
		t.Fatalf("adversarial boundary maximum was %d bytes", maximum)
	}
	t.Logf("five insertion orders yielded one root; maximum all-missed-boundary page=%d bytes (limit=%d)", maximum, metaPageLimit)
}

func TestFB09MetadataBranchGolden(t *testing.T) {
	text, err := os.ReadFile("testdata/fb09/metadata-branch.hex")
	mustOK(t, err)
	data, err := hex.DecodeString(string(bytes.TrimSpace(text)))
	mustOK(t, err)
	level, rows, err := decodeMetadataPage(data)
	mustOK(t, err)
	if level != 1 || len(rows) != 1 || string(rows[0].key) != "b\x00k" || rows[0].count != 7 || rows[0].child[31] != 31 {
		t.Fatalf("branch golden differs: level=%d rows=%+v", level, rows)
	}
	for n := range len(data) {
		if _, _, err := decodeMetadataPage(data[:n]); err == nil {
			t.Fatalf("accepted branch truncation at %d", n)
		}
	}
}

func TestFB09MetadataLeafGolden(t *testing.T) {
	text, err := os.ReadFile("testdata/fb09/metadata-leaf.hex")
	mustOK(t, err)
	data, err := hex.DecodeString(string(bytes.TrimSpace(text)))
	mustOK(t, err)
	level, rows, err := decodeMetadataPage(data)
	mustOK(t, err)
	if level != 0 || len(rows) != 1 || string(rows[0].key) != "b\x00k" {
		t.Fatalf("leaf golden differs: level=%d rows=%+v", level, rows)
	}
	obj := rows[0].object
	if obj == nil || obj.size != 12 || obj.seq != 7 || obj.contentType != "text/plain" ||
		obj.etag != "tag" || obj.manifestUUID != "id" || obj.manifestSHA256[31] != 31 {
		t.Fatalf("leaf golden differs: object=%+v", obj)
	}
	for n := range len(data) {
		if _, _, err := decodeMetadataPage(data[:n]); err == nil {
			t.Fatalf("accepted leaf truncation at %d", n)
		}
	}
}

func FuzzFB09MetadataPage(f *testing.F) {
	for _, name := range []string{"metadata-branch.hex", "metadata-leaf.hex"} {
		data, err := os.ReadFile(filepath.Join("testdata/fb09", name))
		if err != nil {
			f.Fatal(err)
		}
		golden, err := hex.DecodeString(string(bytes.TrimSpace(data)))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(golden)
	}
	f.Add([]byte("ZMP1"))
	f.Fuzz(func(t *testing.T, body []byte) {
		level, rows, err := decodeMetadataPage(body)
		if err != nil {
			return
		}
		if len(body) > metaPageLimit || level > 32 {
			t.Fatal("accepted unbounded metadata page")
		}
		for _, row := range rows {
			if (level == 0) != (row.object != nil) {
				t.Fatal("accepted a metadata page with the wrong row kind")
			}
		}
	})
}

func TestFB09MetadataPageScale(t *testing.T) {
	mode := os.Getenv("ZEROS3_TEST_METADATA_SCALE")
	if mode == "" {
		t.Skip("set ZEROS3_TEST_METADATA_SCALE=1 for generated metadata scaling")
	}
	secret := sha256.Sum256([]byte("FB09 generated scale boundary key"))
	for _, count := range []int{1_000_000, 10_000_000} {
		if count == 10_000_000 && mode == "quick" {
			break
		}
		original := make(map[[32]byte]struct{})
		passes := 1
		if count == 1_000_000 {
			passes = 2
		}
		var flatBytes int64
		for pass := range passes {
			const envelopeBytes = int64(len(metadataArchiveMagic) + metadataArchiveFooter)
			pageBytes, uniqueBytes := envelopeBytes, envelopeBytes
			pages := 0
			tree := newMetadataTree(secret, func(sum [32]byte, body []byte) error {
				n := int64(4 + len(body))
				pages++
				pageBytes += n
				if pass == 0 && passes > 1 {
					original[sum] = struct{}{}
				}
				if _, seen := original[sum]; !seen {
					uniqueBytes += n
				}
				return nil
			})
			out := &nsFileOut{bw: bufio.NewWriter(io.Discard), h: sha256.New()}
			_, err := io.WriteString(out, nsGenMagic)
			mustOK(t, err)
			flat := &nsTableWriter{out: out}
			dict := newNSDict(nil)
			var key, payload []byte
			var tag [32]byte
			entry := objectEntry{manifestUUID: fb09Manifest().ManifestUUID, size: 4096, contentType: "application/octet-stream"}
			for i := range count {
				key = fmt.Appendf(key[:0], "dataset\x00object/%010d", i)
				entry.seq = uint64(i + 1)
				entry.manifestSHA256 = sha256.Sum256(key)
				hex.Encode(tag[:], entry.manifestSHA256[:16])
				entry.etag = string(tag[:])
				if pass > 0 && i%1000 == 0 {
					entry.manifestSHA256[31] ^= 1
				}
				mustOK(t, tree.add(key, &entry))
				if pass == 0 {
					payload = dict.appendObject(payload[:0], &entry)
					mustOK(t, flat.add(key, payload))
				}
			}
			root, err := tree.finish()
			mustOK(t, err)
			if root.rows != uint64(count) {
				t.Fatalf("tree stored %d of %d rows", root.rows, count)
			}
			if pass == 0 {
				mustOK(t, flat.flush())
				var footer bytes.Buffer
				mustOK(t, gob.NewEncoder(&footer).Encode(nsMeta{Obj: flat.dir, Strings: dict.strs}))
				flatBytes = out.n + int64(footer.Len()+16+sha256.Size)
				t.Logf("rows=%d flat=%d archive=%d ratio=%.6f pages=%d root=%x", count, flatBytes, pageBytes, float64(pageBytes)/float64(flatBytes), pages, root.sum)
			} else {
				t.Logf("rows=%d changed=%d new_page_bytes=%d ratio=%.6f root=%x", count, count/1000, uniqueBytes, float64(uniqueBytes)/float64(flatBytes), root.sum)
			}
		}
	}
}
