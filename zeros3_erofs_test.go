package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAuthorityErofsExactRecipeEconomics(t *testing.T) {
	root := os.Getenv("ZEROS3_C5_EROFS")
	if root == "" {
		t.Skip("set ZEROS3_C5_EROFS to the verified image and recipe directory")
	}
	type receipt struct {
		SourceSHA256 string `json:"source_sha256"`
		ImageSHA256  string `json:"image_sha256"`
	}
	type file struct {
		key, path, sha string
	}
	type item struct {
		image, source, recipe file
	}
	var items []item
	for _, level := range []int{9, 12} {
		for _, release := range []string{"9.6", "9.7"} {
			name := fmt.Sprintf("coreutils-%s-hc%d", release, level)
			prefix := filepath.Join(root, name)
			raw, err := os.ReadFile(prefix + ".full.json")
			mustOK(t, err)
			var checks receipt
			mustOK(t, json.Unmarshal(raw, &checks))
			items = append(items, item{
				image:  file{name + ".erofs", prefix + ".erofs", checks.ImageSHA256},
				source: file{release + ".tar", filepath.Join(root, "input-"+release, "source.tar"), checks.SourceSHA256},
				recipe: file{name + ".z3er", prefix + ".z3er", ""},
			})
		}
	}
	type measurement struct {
		AllocatedBytes int64      `json:"allocated_bytes"`
		FileBytes      int64      `json:"file_bytes"`
		ReplayMS       int64      `json:"whole_replay_ms"`
		Plans          []AuthPlan `json:"plans,omitempty"`
		Verified       int        `json:"verified_images"`
	}
	results := map[string]measurement{}
	for _, mode := range []string{"cdc", "b2-delta", "exact-recipe"} {
		t.Run(mode, func(t *testing.T) {
			l := newAuthLab(t)
			ingest := func(f file) {
				r, err := os.Open(f.path)
				mustOK(t, err)
				defer r.Close()
				fi, err := r.Stat()
				mustOK(t, err)
				hash := sha256.New()
				body, err := l.s.ingestStream(io.TeeReader(r, hash), true, ingestOptions{size: fi.Size(), overlapFlush: true})
				mustOK(t, err)
				if f.sha != "" && fmt.Sprintf("%x", hash.Sum(nil)) != f.sha {
					t.Fatalf("unexpected input %s", f.path)
				}
				_, err = l.s.commitIngested("b", f.key, body, "application/octet-stream", nil, putCondition{})
				mustOK(t, err)
			}
			read := func(key string, w io.Writer) {
				size := l.man(key).TotalLength
				rd := l.s.openObjectReader(l.man(key), byteRange{0, size - 1})
				defer rd.close()
				var written int64
				for {
					b, err := rd.next()
					if err == io.EOF {
						break
					}
					mustOK(t, err)
					n, err := w.Write(b)
					mustOK(t, err)
					written += int64(n)
				}
				if written != size {
					t.Fatalf("%s: short reconstructed object", key)
				}
			}
			writtenBases := map[string]bool{}
			for _, i := range items {
				if mode != "exact-recipe" {
					ingest(i.image)
					continue
				}
				if !writtenBases[i.source.key] {
					ingest(i.source)
					writtenBases[i.source.key] = true
				}
				ingest(i.recipe)
			}
			var m measurement
			if mode == "b2-delta" {
				p := l.migrate(AuthPolicy{Strategy: "forced", MinSaving: 0.01, MinObject: 1, AllowDelta: true, NoZN7: true, MaxBases: 1})
				m.Plans = p.Plans
			}
			l.reclaim()
			if os.Getenv("ZEROS3_C5_EROFS_RECOMPRESS") == "1" {
				l.offline(func() {
					_, err := repackStore(l.dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100,
						Compress: true, Recompress: true})
					mustOK(t, err)
				})
			}
			m.FileBytes = dirBytes(t, l.dir)
			allocated, err := exec.Command("du", "-s", "-B1", l.dir).Output()
			mustOK(t, err)
			m.AllocatedBytes, err = strconv.ParseInt(strings.Fields(string(allocated))[0], 10, 64)
			mustOK(t, err)
			l.verifyOK("EROFS payloads after physical reclamation")
			replay := t.TempDir()
			copyObject := func(key string) string {
				name := filepath.Join(replay, key)
				f, err := os.Create(name)
				mustOK(t, err)
				read(key, f)
				mustOK(t, f.Close())
				return name
			}
			t0 := time.Now()
			for _, i := range items {
				h := sha256.New()
				if mode != "exact-recipe" {
					read(i.image.key, h)
				} else {
					recipe, source := copyObject(i.recipe.key), filepath.Join(replay, i.source.key)
					if _, err := os.Stat(source); os.IsNotExist(err) {
						source = copyObject(i.source.key)
					}
					producer := exec.Command(filepath.Join(root, "z4erofslz4"), "-replay", recipe, "-source", source)
					producer.Stdout, producer.Stderr = h, os.Stderr
					mustOK(t, producer.Run())
				}
				if fmt.Sprintf("%x", h.Sum(nil)) != i.image.sha {
					t.Fatalf("%s: image SHA changed", i.image.key)
				}
				m.Verified++
			}
			m.ReplayMS = time.Since(t0).Milliseconds()
			results[mode] = m
		})
	}
	b, err := json.MarshalIndent(results, "", "  ")
	mustOK(t, err)
	t.Log(string(b))
	if out := os.Getenv("ZEROS3_C5_EROFS_OUT"); out != "" {
		mustOK(t, os.WriteFile(out, append(b, '\n'), 0600))
	}
}
