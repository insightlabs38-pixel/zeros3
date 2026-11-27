package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFB10PartPageGoldens(t *testing.T) {
	for _, name := range []string{"part-index", "part-leaf"} {
		t.Run(name, func(t *testing.T) {
			body := fb10JournalGolden(t, name)
			magic, count, rows, err := decodeJournalPartPage(body)
			mustOK(t, err)
			if name == "part-index" && (magic != journalPartPageIndex || count != 129 || rows[31] != 31) ||
				name == "part-leaf" && (magic != journalPartPageLeaf || count != 2 ||
					binary.LittleEndian.Uint32(rows[32:36]) != 16384 ||
					binary.LittleEndian.Uint32(rows[68:72]) != 7) {
				t.Fatal("multipart page golden decoded incorrectly")
			}
			for n := range len(body) {
				if _, _, _, err := decodeJournalPartPage(body[:n]); err == nil {
					t.Fatalf("accepted truncation at %d", n)
				}
			}
		})
	}
}

func FuzzFB10PartPage(f *testing.F) {
	f.Add(fb10JournalGolden(f, "part-index"))
	f.Add(fb10JournalGolden(f, "part-leaf"))
	f.Fuzz(func(t *testing.T, body []byte) {
		magic, count, rows, err := decodeJournalPartPage(body)
		if err != nil {
			return
		}
		reformed := append(journalPartPage(magic, count), rows...)
		if len(body) > journalPartPageLimit || !bytes.Equal(reformed, body) {
			t.Fatal("accepted noncanonical or unbounded multipart page")
		}
	})
}

func TestFB10PartPageReplayAndReconciliation(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	dir := t.TempDir()
	s, err := openStoreWithFeatures(dir, true, true, false, false, false, storeResearch{binary: true, multipartPages: true})
	mustOK(t, err)
	mustOK(t, s.CreateBucket("b"))
	up, err := s.CreateMultipartUpload("b", "part", "application/octet-stream", nil)
	mustOK(t, err)
	start, seq := s.journalEnd()
	body := b4Body(810, 16<<20)
	tag, err := s.UploadPart("b", "part", up, 1, body)
	mustOK(t, err)
	parts, err := s.ListParts("b", "part", up)
	mustOK(t, err)
	if len(parts) != 1 || parts[0].pages == "" || len(parts[0].chunks) < journalPartPageMin {
		t.Fatalf("native UploadPart did not publish a bounded page reference: %+v", parts)
	}
	p := *parts[0]
	_, err = s.PutObject("b", "compact", []byte("root introduced after marking"), "text/plain", nil)
	mustOK(t, err)
	end, _ := s.journalEnd()
	mans, pending, err := s.journalRoots(start, end, seq)
	mustOK(t, err)
	entry, _, err := s.HeadObject("b", "compact")
	mustOK(t, err)
	if len(mans) != 1 || mans[0].ManifestUUID != entry.manifestUUID ||
		mans[0].ManifestSHA256 != fmt.Sprintf("%x", entry.manifestSHA256) ||
		len(pending) != 1 || !slices.Equal(pending[0].parts[1].chunks, p.chunks) {
		t.Fatal("maintenance reconciliation did not retain the exact compact root and all part chunks")
	}
	mustOK(t, s.nsConsolidate(true))
	saved := s.ns.gen.meta.Uploads[0].Parts[0]
	if saved.Pages != p.pages || len(saved.Chunks) != 0 {
		t.Fatal("checkpoint duplicated the out-of-line references")
	}
	mustOK(t, s.Close())

	page := filepath.Join(dir, "journal", journalPartPagesDir, p.pages)
	// A checked, committed journal reference with a missing page is a
	// dependency failure. It must never be treated as a torn suffix.
	mustOK(t, os.Rename(page, page+".saved"))
	if damaged, err := OpenStore(dir); err == nil {
		damaged.Close()
		t.Fatal("reopen silently ignored a missing committed multipart page")
	} else if !strings.Contains(err.Error(), "multipart page") {
		t.Fatalf("wrong damaged page error: %v", err)
	}
	mustOK(t, os.Rename(page+".saved", page))
	s, err = OpenStore(dir)
	mustOK(t, err)
	_, m, err := s.CompleteMultipartUpload("b", "part", up, []completedPart{{PartNumber: 1, ETag: tag}})
	mustOK(t, err)
	_, got, err := s.GetObject("b", "part")
	mustOK(t, err)
	if !bytes.Equal(got, body) || m.ObjectSHA256 != fmt.Sprintf("%x", sha256.Sum256(body)) {
		t.Fatal("paged replay changed the completed bytes")
	}
	mustOK(t, s.Close())
	if old := os.Getenv("ZEROS3_TEST_PRE_PART_PAGES"); old != "" {
		if output, err := exec.Command(old, "verify", "-store", dir).CombinedOutput(); err == nil ||
			!strings.Contains(string(output), journalPartPagesFeature) {
			t.Fatalf("prior J2 reader did not refuse paged store: %v\n%s", err, output)
		}
	}
	t.Logf("native UploadPart verified %d references in checkpoint and exact maintenance closure, then completed %d bytes", len(p.chunks), len(body))
}

func TestFB10PartPagesScale(t *testing.T) {
	if os.Getenv("ZEROS3_TEST_PARTS_SCALE") == "" {
		t.Skip("set ZEROS3_TEST_PARTS_SCALE=1 for the 10,000-part comparison")
	}
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	for _, pageMode := range []bool{false, true} {
		t.Run(fmt.Sprint(pageMode), func(t *testing.T) {
			dir := t.TempDir()
			s, err := openStoreWithFeatures(dir, true, true, false, false, false, storeResearch{multipartPages: pageMode})
			mustOK(t, err)
			mustOK(t, s.CreateBucket("b"))
			id, err := s.CreateMultipartUpload("b", "maximum", "", nil)
			mustOK(t, err)
			data := bytes.Repeat([]byte("local reference"), 1024)
			sum, err := s.casWrite(data)
			mustOK(t, err)
			ing := ingestResult{size: int64(len(data) * 129), etagMD5: md5.Sum(bytes.Repeat(data, 129)), chunks: make([]chunkRef, 129)}
			for i := range ing.chunks {
				ing.chunks[i] = chunkRef{SHA256: fmt.Sprintf("%x", sum), Length: int64(len(data))}
			}
			begin := time.Now()
			for n := 1; n <= maxPartNumber; n++ {
				_, err = s.commitPart("b", "maximum", id, n, ing)
				mustOK(t, err)
			}
			write := time.Since(begin)
			off, seq := s.journalEnd()
			begin = time.Now()
			mustOK(t, s.nsConsolidate(true))
			checkpoint := time.Since(begin)
			info, err := os.Stat(filepath.Join(dir, "journal", nsGenName))
			mustOK(t, err)
			var allocatedPages int64
			folder := filepath.Join(dir, "journal", journalPartPagesDir)
			if pageMode {
				children, err := os.ReadDir(folder)
				mustOK(t, err)
				for _, child := range children {
					info, err := child.Info()
					mustOK(t, err)
					allocatedPages += info.Size()
				}
				if len(children) != 2 {
					t.Fatalf("exact duplicate pages did not converge: %d files", len(children))
				}
			}
			mustOK(t, s.Close())
			for _, useCheckpoint := range []bool{true, false} {
				begin = time.Now()
				s, err = openStore(dir, true, useCheckpoint)
				mustOK(t, err)
				open := time.Since(begin)
				if useCheckpoint && (s.ns.gen == nil || s.ns.gen.meta.Offset != off) {
					t.Fatal("failed to adopt the exact 10,000-part checkpoint")
				}
				list, err := s.ListParts("b", "maximum", id)
				mustOK(t, err)
				if len(list) != maxPartNumber {
					t.Fatalf("replayed %d parts", len(list))
				}
				for n, part := range list {
					if part.partNumber != n+1 || part.size != ing.size || !slices.Equal(part.chunks, ing.chunks) {
						t.Fatalf("part %d changed during replay", part.partNumber)
					}
				}
				t.Logf("paged=%t parts=%d refs=%d journal_bytes=%d sequence=%d checkpoint_bytes=%d page_bytes=%d write=%s consolidate=%s adopted_checkpoint=%t open=%s",
					pageMode, len(list), len(ing.chunks), off, seq, info.Size(), allocatedPages, write, checkpoint, useCheckpoint, open)
				mustOK(t, s.Close())
			}
		})
	}
}

func TestFB10PartPageMultipleLeaves(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	s, err := openStoreWithFeatures(t.TempDir(), true, true, false, false, false, storeResearch{multipartPages: true})
	mustOK(t, err)
	defer s.Close()
	refs := make([]chunkRef, journalPartPageRows+1)
	for i := range refs {
		refs[i] = chunkRef{SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprint(i)))), Length: cdcMinChunkSize}
	}
	size := int64(len(refs) * cdcMinChunkSize)
	root, err := s.writeJournalPartPages(refs, size)
	mustOK(t, err)
	got, err := s.multipartPageRefs(root, nil, size)
	mustOK(t, err)
	if !slices.Equal(got, refs) {
		t.Fatal("multipart page tree changed ordered references across leaves")
	}
	if _, err := s.multipartPageRefs(root, nil, size+1); err == nil {
		t.Fatal("accepted different part size for the same children")
	}
	dir := filepath.Join(s.root, "journal", journalPartPagesDir)
	files, err := os.ReadDir(dir)
	mustOK(t, err)
	if len(files) != 3 {
		t.Fatalf("expected one index and two leaves, got %d files", len(files))
	}
	for _, file := range files {
		info, err := file.Info()
		mustOK(t, err)
		if info.Size() > journalPartPageLimit {
			t.Fatalf("page exceeded its physical cap: %d bytes", info.Size())
		}
	}
}
