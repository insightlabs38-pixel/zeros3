package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP8QwenControl(t *testing.T) {
	source, dir := os.Getenv("ZEROS3_P8_QWEN_DIR"), os.Getenv("ZEROS3_P8_QWEN_STORE")
	if source == "" || dir == "" {
		t.Skip("set ZEROS3_P8_QWEN_DIR and ZEROS3_P8_QWEN_STORE")
	}
	pins, err := os.ReadFile("testing-harnesses/z4/b5/pins.json")
	if err != nil {
		t.Fatal(err)
	}
	var inputs []struct {
		Local, SHA256 string
		Size          int64
	}
	if err := json.Unmarshal(pins, &inputs); err != nil {
		t.Fatal(err)
	}
	readback := os.Getenv("ZEROS3_P8_QWEN_CHECK") != ""
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "0")
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !readback {
		if err := s.CreateBucket("models"); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for _, input := range inputs {
		if input.Local != "qwen05/base.safetensors" && input.Local != "qwen05/instruct.safetensors" {
			continue
		}
		count++
		name := filepath.Base(input.Local)
		t.Run(name, func(t *testing.T) {
			h := sha256.New()
			if !readback {
				f, err := os.Open(filepath.Join(source, name))
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				st, err := f.Stat()
				if err != nil || st.Size() != input.Size {
					t.Fatalf("invalid pinned size for %s: %v", name, err)
				}
				ing, err := s.ingestStream(io.TeeReader(f, h), true, ingestOptions{size: input.Size})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.commitIngested("models", name, ing, "application/octet-stream", nil, putCondition{}); err != nil {
					t.Fatal(err)
				}
			} else {
				entry, _, err := s.headServed("models", name)
				if err != nil || entry.size != input.Size {
					t.Fatalf("wrong visible object size for %s: %v", name, err)
				}
				m, err := s.readVerifiedManifest(entry.manifestUUID, entry.manifestSHA256)
				if err != nil {
					t.Fatal(err)
				}
				r := s.openObjectReader(m, byteRange{0, input.Size - 1})
				defer r.close()
				var logical int64
				for {
					b, err := r.next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					logical += int64(len(b))
					h.Write(b)
				}
				if logical != input.Size {
					t.Fatalf("read %d bytes, want %d", logical, input.Size)
				}
			}
			if got := fmt.Sprintf("%x", h.Sum(nil)); !strings.EqualFold(got, input.SHA256) {
				t.Fatalf("SHA-256 differs for %s: %s", name, got)
			}
			t.Logf("verified %s bytes=%d sha256=%s rss_kib=%d", name, input.Size, input.SHA256, vmHWM()>>10)
		})
	}
	if count != 2 {
		t.Fatalf("found %d pinned controls, want 2", count)
	}
}
