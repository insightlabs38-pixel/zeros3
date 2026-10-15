package composez3

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"runtime"
	"testing"

	"zeros3-z3/fstate"
	"zeros3-z3/lab"
	"zeros3-z3/ociz3"
)

func testELF(t *testing.T) []byte {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("needs an ELF executable (the test binary)")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil || !bytes.HasPrefix(b, []byte("\x7fELF")) {
		t.Skip("test binary is not ELF")
	}
	return b
}

func layer(t *testing.T, members map[string][]byte, order []string) fstate.File {
	t.Helper()
	var ms []ociz3.Member
	for _, n := range order {
		ms = append(ms, ociz3.Member{Path: n, Mode: 0o644, MTime: 1700000000, Data: members[n]})
	}
	tb, err := ociz3.WriteTar(ms, tar.FormatPAX)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := ociz3.Compress(tb, ociz3.GzipOpts{})
	if err != nil {
		t.Fatal(err)
	}
	return fstate.File{Name: "layer.tgz", Kind: string(ociz3.KindLayerGz), Data: gz}
}

func fixtures(t *testing.T) (map[string][]byte, []string) {
	db, err := os.ReadFile("../sqlitez3/testdata/ps4096.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	car, err := os.ReadFile("../carz3/testdata/sample-rw-bs-v2.car")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string][]byte{"etc/passwd": []byte("root:x:0:0:root:/root:/bin/sh\n"), "bin/app": testELF(t), "data/app.db": db, "data/blocks.car": car,
		"data/junk.bin": append([]byte("\x7fELF"), bytes.Repeat([]byte{0xff}, 100000)...)} // looks like an ELF, is not
	return m, []string{"etc/passwd", "bin/app", "data/app.db", "data/blocks.car", "data/junk.bin"}
}

func composedRep(rep *Report, opt Options) fstate.RepDef {
	return fstate.RepDef{Name: "composed", Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) {
		outer, err := ociz3.TransformPlan(f, false)
		if err != nil {
			return nil, err
		}
		p, r, err := Compose(outer, opt)
		if err != nil {
			return nil, err
		}
		*rep = *r
		if a := Audit(p); len(a) > 0 {
			return nil, errors.New(a[0])
		}
		return p, nil
	}}
}

func TestComposedLayerIsExact(t *testing.T) {
	ms, order := fixtures(t)
	f1 := layer(t, ms, order)
	ms2 := map[string][]byte{}
	for k, v := range ms {
		ms2[k] = v
	}
	db2 := append([]byte(nil), ms["data/app.db"]...)
	db2[9000] ^= 0x55
	ms2["data/app.db"] = db2
	f2 := layer(t, ms2, order)
	f2.Name = "layer2.tgz"

	var rep Report
	r := &fstate.Runner{RefBytes: 96}
	rows := r.RunStates([]*fstate.State{{Label: "a", Files: []fstate.File{f1}}, {Label: "b", Files: []fstate.File{f2}}},
		[]fstate.RepDef{composedRep(&rep, Options{MinSize: 512})})
	for _, x := range rows {
		if !x.Exact || x.Err != "" {
			t.Fatalf("%s: exact=%v err=%q", x.State, x.Exact, x.Err)
		}
	}
	if rep.Substituted != 3 || rep.Fallback != 1 || rep.ByKind[KindExec] != 1 || rep.ByKind[KindSQLite] != 1 || rep.ByKind[KindCAR] != 1 {
		t.Fatalf("substituted %+v (the fake ELF must stay with the outer representation or fall back)", rep)
	}
	// the second layer shares the unchanged executable's inner components: it costs far less than the first
	if rows[1].NewBytes >= rows[0].NewBytes/4 {
		t.Errorf("second layer new bytes %d vs first %d", rows[1].NewBytes, rows[0].NewBytes)
	}
}

func TestCompositionRecordBoundary(t *testing.T) {
	ms, order := fixtures(t)
	f := layer(t, ms, order)
	outer, err := ociz3.TransformPlan(f, false)
	if err != nil {
		t.Fatal(err)
	}
	n := len(outer.Pieces)
	p, rep, err := Compose(outer, Options{MinSize: 512})
	if err != nil || rep.Substituted == 0 {
		t.Fatal(err, rep)
	}
	acct := lab.NewAcct(96, false)
	if _, err := acct.Add(p); err != nil {
		t.Fatal(err)
	}
	rec := p.Detail["composition_record"].([]byte)
	var ok bytes.Buffer
	if err := rebuildComposed(rec, outer, n, p.Pieces, &ok, acct.Get); err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	if err := p.Rebuild(&want, acct.Get); err != nil || !bytes.Equal(ok.Bytes(), want.Bytes()) {
		t.Fatal("rebuilds differ", err)
	}
	// every truncation and every single-byte mutation of the record is rejected or yields a different, checked failure — never a silent wrong object
	for k := 0; k < len(rec); k++ {
		if err := rebuildComposed(rec[:k], outer, n, p.Pieces, &bytes.Buffer{}, acct.Get); err == nil {
			t.Fatalf("record truncated to %d accepted", k)
		}
	}
	for k := range rec {
		m := append([]byte(nil), rec...)
		m[k] ^= 0x41
		var out bytes.Buffer
		if err := rebuildComposed(m, outer, n, p.Pieces, &out, acct.Get); err == nil && !bytes.Equal(out.Bytes(), ok.Bytes()) {
			t.Fatalf("mutated record at %d produced a different object without error", k)
		}
	}
	// stored inner components: a tampered one and a missing one fail
	bad := func(id lab.ID) ([]byte, error) {
		b, err := acct.Get(id)
		if err == nil && len(b) > 100 {
			b = append([]byte(nil), b...)
			b[len(b)/2] ^= 1
		}
		return b, err
	}
	if err := rebuildComposed(rec, outer, n, p.Pieces, &bytes.Buffer{}, bad); err == nil {
		t.Error("tampered components accepted")
	}
	if err := rebuildComposed(rec, outer, n, p.Pieces, &bytes.Buffer{}, func(lab.ID) ([]byte, error) { return nil, errors.New("gone") }); err == nil {
		t.Error("missing components accepted")
	}
}

func TestDepthIsBounded(t *testing.T) {
	ms, order := fixtures(t)
	outer, _ := ociz3.TransformPlan(layer(t, ms, order), false)
	if _, _, err := Compose(outer, Options{Depth: 2}); !errors.Is(err, ErrDepth) {
		t.Fatalf("depth 2 outer accepted: %v", err)
	}
	if _, _, err := parseRecord(marshalRecord(2, nil), 1); !errors.Is(err, ErrDepth) {
		t.Fatalf("record with depth 2 accepted: %v", err)
	}
	if _, _, err := Compose(&lab.Plan{}, Options{}); err == nil {
		t.Fatal("non-exact outer accepted")
	}
}

func TestInnerFallbackKeepsOuterExact(t *testing.T) {
	ms, order := fixtures(t)
	f := layer(t, ms, order)
	outer, _ := ociz3.TransformPlan(f, false)
	// forbid every kind: nothing is substituted, the plan is the outer plan
	p, rep, err := Compose(outer, Options{MinSize: 512, Kinds: []Kind{99}})
	if err != nil || rep.Substituted != 0 || p != outer {
		t.Fatalf("%v %+v", err, rep)
	}
	for _, k := range []Kind{KindExec, KindSQLite, KindCAR} {
		if _, err := Encode(k, []byte("definitely not that format")); err == nil {
			t.Errorf("%v accepted garbage", k)
		}
	}
	for _, b := range [][]byte{nil, {1}, bytes.Repeat([]byte{0x7f}, 100)} {
		if k, ok := Detect(b); ok && k == KindSQLite {
			t.Error("garbage detected as sqlite")
		}
	}
}

func TestParseRecordHostile(t *testing.T) {
	good := marshalRecord(1, []sub{{idx: 3, kind: KindExec, pre: "blob 12\x00", pad: 7, rid: lab.ID{1}}, {idx: 9, kind: KindCAR}})
	if _, s, err := parseRecord(good, 10); err != nil || len(s) != 2 {
		t.Fatal(err)
	}
	for k := 0; k < len(good); k++ {
		if _, _, err := parseRecord(good[:k], 10); err == nil {
			t.Fatalf("truncation %d accepted", k)
		}
	}
	if _, _, err := parseRecord(good, 5); err == nil { // piece index out of range
		t.Fatal("out-of-range index accepted")
	}
	huge := append([]byte("CMP1\x01"), 0xff, 0xff, 0xff, 0xff, 0x0f)
	if _, _, err := parseRecord(huge, 10); err == nil {
		t.Fatal("hostile count accepted")
	}
}
