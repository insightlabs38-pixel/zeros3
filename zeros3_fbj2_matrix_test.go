package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestFBJ2PrototypeRecovery(t *testing.T) {
	for _, mode := range []byte{'A', 'B', 'C'} {
		t.Run(string(mode), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "groups")
			p, err := fbj2Create(path, mode)
			if err != nil {
				t.Fatal(err)
			}
			defer p.file.Close()
			payloads := [][]byte{bytes.Repeat([]byte("a"), 9000), bytes.Repeat([]byte("b"), 8000), []byte("complete")}
			positions := []fbj2Cursor{p.pos}
			for range 3 {
				if err := p.append(payloads); err != nil {
					t.Fatal(err)
				}
				positions = append(positions, p.pos)
			}
			if p.syncs != 3 || p.writes != 3*(1+btoi(mode == 'C')) {
				t.Fatalf("writes=%d syncs=%d", p.writes, p.syncs)
			}
			full := fb01rRead(t, path)
			previous := bytes.Clone(full[:positions[2].end])
			if mode == 'C' {
				copy(previous[v2SlotStride:], fbj2Witness(positions[1]))
			}
			if got, verdict := fbj2Recover(mode, previous); got.count != 2 || verdict != "clean" {
				t.Fatalf("prior witness and body: count=%d %s", got.count, verdict)
			}
			type testImage struct {
				name    string
				bytes   []byte
				acked   uint64
				visible uint64
				want    string
			}
			cases := []testImage{
				{"post-sync", full, 3, 3, "clean"},
				{"complete-before-ack", full, 2, 3, "clean"},
			}
			for _, cut := range []int{1, 512, 4096, int(positions[3].end-positions[2].end) - 1} {
				image := append(bytes.Clone(previous), full[positions[2].end:positions[2].end+uint64(cut)]...)
				cases = append(cases, testImage{fmt.Sprintf("pre-sync-prefix-%d", cut), image, 2, 2, "recover"})
			}
			rand := rand.New(rand.NewPCG(123, 426))
			for i := range 24 {
				image := append(bytes.Clone(previous), make([]byte, positions[3].end-positions[2].end)...)
				// Pick arbitrary surviving unsynced sectors. Force one
				// absent sector inside the group's covered bytes.
				past := int(positions[2].end)
				for off := past; off < len(image); off += 512 {
					if rand.IntN(2) == 1 && off != past+512 {
						end := min(len(image), off+512)
						copy(image[off:end], full[off:end])
					}
				}
				cases = append(cases, testImage{fmt.Sprintf("pre-sync-sectors-%02d", i), image, 2, 2, "recover"})
			}
			earlyDamage := bytes.Clone(full)
			earlyDamage[positions[0].end+150] ^= 1
			earlyVerdict := "refuse-later"
			if mode == 'C' {
				earlyVerdict = "refuse-witness"
			}
			cases = append(cases, testImage{"old-body-corrupt", earlyDamage, 3, 0, earlyVerdict})
			lastDamage := bytes.Clone(full)
			lastDamage[len(lastDamage)-1] ^= 1
			lastVerdict, cutVerdict := "recover", "clean"
			if mode == 'C' {
				lastVerdict, cutVerdict = "refuse-witness", "refuse-witness"
				ahead := bytes.Clone(previous)
				copy(ahead[v2SlotStride:], full[v2SlotStride:v2SlotStride+fbj2Slot])
				behind := append(bytes.Clone(previous), full[positions[2].end:]...)
				both := bytes.Clone(lastDamage)
				both[v2SlotStride+fbj2Slot-1] ^= 1
				cases = append(cases,
					testImage{"pre-sync-witness-ahead", ahead, 2, 2, "refuse-witness"},
					testImage{"pre-sync-body-ahead", behind, 2, 3, "clean"},
					testImage{"last-witness-and-footer-corrupt", both, 3, 2, "refuse-degraded"})
			}
			cases = append(cases,
				testImage{"acknowledged-last-footer-corrupt", lastDamage, 3, 2, lastVerdict},
				testImage{"acknowledged-boundary-cut", full[:positions[2].end], 3, 2, cutVerdict},
				testImage{"durable-prefix-and-witness-rollback", previous, 3, 2, "clean"})
			for _, tc := range cases {
				got, verdict := fbj2Recover(mode, tc.bytes)
				if got.count != tc.visible || verdict != tc.want {
					t.Errorf("%s: count=%d outcome=%s, want %d %s", tc.name, got.count, verdict, tc.visible, tc.want)
				}
				if os.Getenv("FBJ2_REPORT") != "" {
					evidence := "covered"
					if got.count < tc.acked && verdict != "clean" && verdict != "recover" {
						evidence = "refused"
					} else if got.count < tc.acked {
						evidence = "UNDETECTABLE"
					} else if verdict != "clean" && verdict != "recover" {
						evidence = "availability-cost"
					}
					fbj2Report(map[string]any{"kind": "recovery", "candidate": string(mode), "case": tc.name,
						"acknowledged_groups": tc.acked, "parsed_groups": got.count, "decision": verdict, "classification": evidence})
				}
			}
		})
	}
}

func fbj2Report(row any) {
	data, err := json.Marshal(row)
	if err != nil {
		panic(err)
	}
	fmt.Printf("FBJ2 %s\n", data)
}

func TestFBJ2JournalMicro(t *testing.T) {
	if os.Getenv("FBJ2_BENCH") != "1" {
		t.Skip("opt-in paired microbenchmark")
	}
	const groups = 256
	modes := []string{"v1-off", "v1-lazy", "v1-sync", "old-piggyback", "old-sync", "A", "B", "C"}
	for _, width := range []int{1, 4, 8, 32} {
		recs := make([]journalFrameIn, width)
		payloads := make([][]byte, width)
		for i := range recs {
			payloads[i] = bytes.Repeat([]byte{byte(i)}, 300)
			recs[i] = journalFrameIn{recType: recordTypePutObjectRoot, payload: payloads[i]}
		}
		for round := range 3 {
			for n := range modes {
				mode := modes[(n+round*3)%len(modes)]
				path := filepath.Join(t.TempDir(), "groups")
				var appendGroup func() error
				var closeFile func() error
				writes, syncs := 0, 0
				switch mode {
				case "v1-off", "v1-lazy", "v1-sync":
					t.Setenv("ZEROS3_JOURNAL_WATERMARK", mode[3:])
					j, _, err := openJournalOpts(path, journalOpenOpts{storeID: "fbj2-micro"})
					if err != nil {
						t.Fatal(err)
					}
					appendGroup = func() error { _, err := j.appendFrames(recs); return err }
					closeFile = j.close
					writes, syncs = groups, groups
					if mode != "v1-off" {
						writes += groups
					}
					if mode == "v1-sync" {
						syncs += groups
					}
				case "old-piggyback", "old-sync":
					j, err := v2Create(path, mode == "old-sync")
					if err != nil {
						t.Fatal(err)
					}
					appendGroup = func() error { return j.append(payloads) }
					closeFile = j.f.Close
				default:
					p, err := fbj2Create(path, mode[0])
					if err != nil {
						t.Fatal(err)
					}
					appendGroup = func() error { return p.append(payloads) }
					closeFile = p.file.Close
				}
				ack := make([]time.Duration, groups)
				start := time.Now()
				for i := range ack {
					at := time.Now()
					if err := appendGroup(); err != nil {
						t.Fatal(err)
					}
					ack[i] = time.Since(at)
				}
				elapsed := time.Since(start)
				if err := closeFile(); err != nil {
					t.Fatal(err)
				}
				sort.Slice(ack, func(i, j int) bool { return ack[i] < ack[j] })
				st, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				image := fb01rRead(t, path)
				at := time.Now()
				switch mode {
				case "v1-off", "v1-lazy", "v1-sync":
					scan, _, err := scanJournalRange(bytes.NewReader(image), 0, int64(len(image)), 0, false, nil)
					if err != nil || scan.Damage != nil || scan.LastSeq != uint64(groups*width) {
						t.Fatalf("v1 replay failed: %s %+v %v", mode, scan, err)
					}
				case "old-piggyback", "old-sync":
					if got := v2Recover(image); got.verdict != "ok" || got.keep != int64(len(image)) {
						t.Fatalf("old Design B replay: %+v", got)
					}
					writes, syncs = groups*2, groups*(1+btoi(mode == "old-sync"))
				default:
					if got, verdict := fbj2Recover(mode[0], image); got.count != groups || verdict != "clean" {
						t.Fatalf("%s replay: groups=%d %s", mode, got.count, verdict)
					}
					writes, syncs = groups*(1+btoi(mode == "C")), groups
				}
				fbj2Report(map[string]any{"kind": "journal-micro", "mode": mode, "width": width, "round": round,
					"groups": groups, "elapsed_ns": elapsed.Nanoseconds(), "operations_per_second": float64(groups*width) / elapsed.Seconds(),
					"ack_p50_ns": ack[groups/2].Nanoseconds(), "ack_p95_ns": ack[groups*95/100].Nanoseconds(),
					"ack_p99_ns": ack[groups*99/100].Nanoseconds(), "writes": writes, "syncs": syncs,
					"logical_bytes": st.Size(), "memory_scan_ns": time.Since(at).Nanoseconds(),
					"metrics_scope": "ack-path calls only; replay from RAM"})
			}
		}
	}
}
