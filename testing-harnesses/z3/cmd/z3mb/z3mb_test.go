package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"zeros3-z3/dexz3"
	"zeros3-z3/jvmz3"
	"zeros3-z3/lab"
)

type member struct {
	name   string
	data   []byte
	stored bool
}

func mkZip(t *testing.T, ms []member) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, m := range ms {
		method := zip.Deflate
		if m.stored {
			method = zip.Store
		}
		f, err := w.CreateHeader(&zip.FileHeader{Name: m.name, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		f.Write(m.data)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withSigningBlock inserts opaque bytes (an APK Signing Block lookalike) between the last local
// entry and the central directory and repairs the end-of-central-directory offset.
func withSigningBlock(z []byte, block []byte) []byte {
	eocd := len(z) - 22
	cd := int(binary.LittleEndian.Uint32(z[eocd+16:]))
	out := append(append(append([]byte(nil), z[:cd]...), block...), z[cd:]...)
	binary.LittleEndian.PutUint32(out[len(out)-22+16:], uint32(cd+len(block)))
	return out
}

func read(t *testing.T, p ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(p...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func text(n int, s string) []byte { return bytes.Repeat([]byte(s), n) }

// The composition layer reuses the lab's exact ZIP framing and exact DEFLATE replay and only swaps
// the plaintext representation of DEX and class members: every container must rebuild byte for
// byte from the store, whatever it holds.
func TestContainersRebuildExactly(t *testing.T) {
	dex28 := read(t, "..", "..", "dexz3", "testdata", "fixture-api28.dex")
	dex24 := read(t, "..", "..", "dexz3", "testdata", "fixture-api24.dex")
	cls := read(t, "..", "..", "jvmz3", "testdata", "classes", "p", "Managed.class")
	cls2 := read(t, "..", "..", "jvmz3", "testdata", "classes", "p", "Base.class")
	mod := read(t, "..", "..", "jvmz3", "testdata", "classes", "module-info.class")
	badDex := append([]byte("dex\n035\x00"), bytes.Repeat([]byte{7}, 400)...)
	futureDex := append([]byte(nil), dex28...)
	futureDex[5], futureDex[6] = '9', '9'
	badClass := append([]byte{0xCA, 0xFE, 0xBA, 0xBE}, bytes.Repeat([]byte{1}, 300)...)

	apk := []member{{"AndroidManifest.xml", text(300, "<manifest/>"), false}, {"classes.dex", dex28, false}, {"resources.arsc", text(900, "arsc"), true}, {"assets/a.txt", text(500, "asset"), false}}
	cases := []struct {
		name string
		z    []byte
		dex  int // members expected to take the inner path
		cls  int
	}{
		{"DEX in an APK, DEFLATE", mkZip(t, apk), 1, 0},
		{"DEX in an APK, stored", mkZip(t, []member{{"classes.dex", dex28, true}, {"a.txt", text(300, "x"), false}}), 1, 0},
		{"two DEX members (multidex)", mkZip(t, []member{{"classes.dex", dex28, false}, {"classes2.dex", dex24, false}}), 2, 0},
		{"member order reversed", mkZip(t, []member{apk[3], apk[2], apk[1], apk[0]}), 1, 0},
		{"opaque signing block", withSigningBlock(mkZip(t, apk), append([]byte("APK Sig Block 42"), bytes.Repeat([]byte{9}, 700)...)), 1, 0},
		{"classes in a JAR", mkZip(t, []member{{"META-INF/MANIFEST.MF", text(60, "Manifest-Version: 1.0\r\n"), false}, {"p/Managed.class", cls, false}, {"p/Base.class", cls2, false}, {"module-info.class", mod, true}}), 0, 3},
		{"corrupt and unsupported members fall back", mkZip(t, []member{{"classes.dex", badDex, false}, {"classes2.dex", futureDex, false}, {"A.class", badClass, false}, {"ok.class", cls, false}}), 0, 1},
		{"no managed bytecode at all", mkZip(t, []member{{"a", text(500, "plain"), false}, {"b", text(50, "x"), true}}), 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &innerStats{}
			reps := containerReps([]string{"outer", "outer+both-unit4k"}, st)
			if len(reps) != 2 {
				t.Fatalf("reps: %d", len(reps))
			}
			for _, r := range reps {
				store := newStore()
				a := store.Run(lab.EconRep{Name: r.Name, Plan: func(f lab.EconFile) (*lab.Plan, bool, error) {
					p, fb, err := r.Plan(f)
					return treeify(p, 8), fb, err
				}}, []lab.EconFile{{Name: "pkg", Data: c.z}})
				if !a.Exact {
					t.Fatalf("%s: not exact: %s", r.Name, a.Err)
				}
				if a.Fallback != 0 && r.Name == "outer" {
					t.Fatalf("outer fell back to CDC")
				}
			}
			// the inner transform took exactly the members it should (and only those)
			if got := int(st.ok.Load()); got != c.dex+c.cls {
				t.Errorf("inner transform handled %d members, want %d (dex %d, class %d); fallbacks %d", got, c.dex+c.cls, c.dex, c.cls, st.fallback.Load())
			}
		})
	}
	// a second layout of the same members reuses the stored components
	st := &innerStats{}
	r := containerReps([]string{"outer+both-unit4k"}, st)[0]
	store := newStore()
	run := func(z []byte) lab.EconAgg {
		return store.Run(lab.EconRep{Name: r.Name, Plan: func(f lab.EconFile) (*lab.Plan, bool, error) { return r.Plan(f) }}, []lab.EconFile{{Name: string(z[:12]) + "x", Data: z}})
	}
	first := run(mkZip(t, apk))
	second := run(mkZip(t, []member{apk[3], apk[1], apk[0], apk[2]}))
	if !first.Exact || !second.Exact {
		t.Fatal("not exact")
	}
	if second.PackedNew*4 > first.PackedNew {
		t.Errorf("reordered members re-stored %d of %d packed bytes", second.PackedNew, first.PackedNew)
	}
	_ = dexz3.HeaderSize
	_ = jvmz3.TagUtf8
}

func TestCuttersAreMonotoneAndComplete(t *testing.T) {
	b := read(t, "..", "..", "dexz3", "testdata", "fixture-api28.dex")
	for _, c := range []cutter{cdcProd(), cdcScaled(1), sectionCDC(1), sections(), unitCut(1), perItem(dexz3.TypeCode)} {
		rep, err := encodeWith(b, c)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		prev := 0
		for _, e := range rep.Cuts {
			if e <= prev && prev != 0 {
				t.Fatalf("%s: cuts not increasing", c.Name)
			}
			prev = e
		}
		if prev != rep.Stats.Payload {
			t.Fatalf("%s: cuts end at %d of %d", c.Name, prev, rep.Stats.Payload)
		}
	}
}

func encodeWith(b []byte, c cutter) (*dexz3.Rep, error) {
	f, err := dexz3.Parse(bytes.NewReader(b), int64(len(b)), dexz3.Limits{})
	if err != nil {
		return nil, err
	}
	return dexz3.Encode(f, b, dexz3.Config{Cut: c.Cut})
}
