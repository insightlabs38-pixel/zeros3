package repz3

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"testing"
)

func concatOf(st *MemStore, parts ...[]byte) (SHA, *Rep) {
	r := &Rep{Kind: KindConcat}
	for _, p := range parts {
		id, _ := st.Put(p)
		r.Members = append(r.Members, Member{Role: Stored, SHA: id, Size: int64(len(p))})
		r.Size += int64(len(p))
	}
	return put(st, r), r
}

func put(st *MemStore, r *Rep) SHA {
	b, err := r.Marshal()
	if err != nil {
		panic(err)
	}
	id, _ := st.Put(b)
	return id
}

// derive wraps child output in a frame inside a new concat representation.
func derive(st *MemStore, childID SHA, childSize int64, f Frame, framed []byte) (SHA, *Rep) {
	r := &Rep{Kind: KindConcat, Size: int64(len(framed)), Members: []Member{{Role: Derived, SHA: sha256.Sum256(framed), Size: int64(len(framed)), Child: childID, Frame: f}}}
	return put(st, r), r
}

func rebuild(e *Env, id SHA) ([]byte, error) {
	var b bytes.Buffer
	_, _, err := e.Reconstruct(id, &b)
	return b.Bytes(), err
}

// golden is the byte-exact wire form of a small representation. If it changes the
// contract changed: bump Version.
const golden = "5a335250" + "02" + "01" + "0a" + "02" + // magic, version 2, kind concat, size 10, 2 members
	"01" + "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" + "00" + // empty stored member
	"01" + "84d89877f0d4041efb6bf91a16f0248f2fd573e6af05c19f96bedb9f882f7882" + "0a" + // 10-byte member
	"00" + "00" // no recipe, no index

func TestMarshalIsDeterministicAndGolden(t *testing.T) {
	r := &Rep{Kind: KindConcat, Size: 10, Members: []Member{
		{Role: Stored, SHA: sha256.Sum256(nil)},
		{Role: Stored, SHA: sha256.Sum256([]byte("0123456789")), Size: 10},
	}}
	b, err := r.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(b); got != golden {
		t.Fatalf("wire form changed:\n got %s\nwant %s", got, golden)
	}
	b2, _ := r.Marshal()
	if !bytes.Equal(b, b2) {
		t.Fatal("two marshals differ")
	}
	back, err := Unmarshal(b, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	b3, _ := back.Marshal()
	if !bytes.Equal(b, b3) {
		t.Fatal("round trip changed the bytes")
	}
}

func fullRep(st *MemStore) *Rep {
	child, _ := concatOf(st, []byte("abc"))
	framed := append(append([]byte("P:"), "abc"...), 0, 0, 0)
	ix, _ := st.Put(append(bytes.Repeat([]byte{1}, 8), make([]byte, 32)...))
	return &Rep{Kind: KindCAR, Size: 99, Recipe: []byte("recipe-bytes"),
		Members: []Member{
			{Role: Stored, SHA: sha256.Sum256([]byte("x")), Size: 1},
			{Role: Derived, SHA: sha256.Sum256(framed), Size: int64(len(framed)), Child: child, Frame: Frame{Prefix: []byte("P:"), Zeros: 3}},
		},
		Indexes: []Index{{KeyWidth: 8, Count: 1, Ref: ix, Owns: true}}}
}

func TestEveryTruncationFails(t *testing.T) {
	b, err := fullRep(NewMemStore()).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Unmarshal(b, Limits{}); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < len(b); n++ {
		if _, err := Unmarshal(b[:n], Limits{}); err == nil {
			t.Fatalf("truncation to %d of %d bytes parsed", n, len(b))
		}
	}
	if _, err := Unmarshal(append(append([]byte(nil), b...), 0), Limits{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("trailing byte: %v", err)
	}
	for i := range b { // no single-byte mutation may panic
		c := append([]byte(nil), b...)
		c[i] ^= 0xff
		Unmarshal(c, Limits{})
	}
}

func TestMalformedHeaders(t *testing.T) {
	good, _ := fullRep(NewMemStore()).Marshal()
	mut := func(f func(b []byte) []byte) []byte { return f(append([]byte(nil), good...)) }
	cases := []struct {
		name string
		b    []byte
		want error
	}{
		{"magic", mut(func(b []byte) []byte { b[0] = 'X'; return b }), ErrCorrupt},
		{"unknown version", mut(func(b []byte) []byte { b[4] = 3; return b }), ErrUnsupported},
		{"obsolete version 1", mut(func(b []byte) []byte { b[4] = 1; return b }), ErrUnsupported},
		{"index flags", mut(func(b []byte) []byte { b[len(b)-35] = 0x80; return b }), ErrCorrupt},
		{"unknown kind", mut(func(b []byte) []byte { b[5] = 99; return b }), ErrUnsupported},
		{"kind zero", mut(func(b []byte) []byte { b[5] = 0; return b }), ErrUnsupported},
		{"unterminated varint", append([]byte("Z3RP\x02\x01"), bytes.Repeat([]byte{0xff}, 12)...), ErrCorrupt},
		{"non-minimal varint", []byte("Z3RP\x02\x01\x80\x00\x00\x00\x00"), ErrCorrupt},
		{"size over int64", append([]byte("Z3RP\x02\x01"), 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01), ErrCorrupt},
		{"excessive member count", append([]byte("Z3RP\x02\x01\x00"), 0xff, 0xff, 0xff, 0x7f), ErrLimit},
		{"role", mut(func(b []byte) []byte { b[8] = 7; return b }), ErrCorrupt},
		{"via on the wire", mut(func(b []byte) []byte { b[8] = byte(Via); return b }), ErrCorrupt},
	}
	for _, c := range cases {
		if _, err := Unmarshal(c.b, Limits{}); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	// a member count the remaining bytes cannot hold must not allocate
	if _, err := Unmarshal(append([]byte("Z3RP\x02\x01\x00"), 0xff, 0xff, 0xff, 0x0f), Limits{MaxMembers: 1 << 30}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("huge count: %v", err)
	}
	// recipe longer than the object
	r := &Rep{Kind: KindCAR}
	b, _ := r.Marshal()
	b = append(b[:len(b)-2], 0xff, 0xff, 0x7f, 0)
	if _, err := Unmarshal(b, Limits{}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("long recipe: %v", err)
	}
	if _, err := Unmarshal(b, Limits{MaxRepBytes: 16}); err == nil {
		t.Error("recipe over MaxRepBytes parsed")
	}
}

func tooManySections() []Index {
	var x []Index
	for i := 0; i <= maxIndexes; i++ {
		x = append(x, Index{KeyWidth: 8, Count: 1, Ref: SHA{byte(i + 1)}})
	}
	return x
}

func TestValidateRejects(t *testing.T) {
	a, b := sha256.Sum256([]byte("a")), sha256.Sum256([]byte("b"))
	bad := map[string]*Rep{
		"negative size":         {Kind: KindCAR, Size: -1},
		"negative member":       {Kind: KindCAR, Members: []Member{{Role: Stored, SHA: a, Size: -1}}},
		"same SHA two lengths":  {Kind: KindCAR, Members: []Member{{Role: Stored, SHA: a, Size: 1}, {Role: Stored, SHA: a, Size: 2}}},
		"via":                   {Kind: KindCAR, Members: []Member{{Role: Via, SHA: a}}},
		"member sizes overflow": {Kind: KindConcat, Size: 0, Members: []Member{{Role: Stored, SHA: a, Size: 1<<63 - 1}, {Role: Stored, SHA: b, Size: 5}}},
		"concat total":          {Kind: KindConcat, Size: 3, Members: []Member{{Role: Stored, SHA: a, Size: 1}}},
		"concat recipe":         {Kind: KindConcat, Size: 1, Recipe: []byte{1}, Members: []Member{{Role: Stored, SHA: a, Size: 1}}},
		"frame over length":     {Kind: KindCAR, Members: []Member{{Role: Derived, SHA: a, Size: 4, Child: b, Frame: Frame{Prefix: []byte("xyz"), Zeros: 2}}}},
		"frame zeros bound":     {Kind: KindCAR, Members: []Member{{Role: Derived, SHA: a, Size: 1 << 40, Child: b, Frame: Frame{Zeros: maxZeros + 1}}}},
		"frame prefix bound":    {Kind: KindCAR, Members: []Member{{Role: Derived, SHA: a, Size: 1 << 40, Child: b, Frame: Frame{Prefix: make([]byte, maxPrefix+1)}}}},
		"index geometry":        {Kind: KindCAR, Indexes: []Index{{KeyWidth: 0, Count: 1}}},
		"index count":           {Kind: KindCAR, Indexes: []Index{{KeyWidth: 8, Count: maxIndexCount + 1}}},
		"duplicate section":     {Kind: KindCAR, Indexes: []Index{{KeyWidth: 8, Count: 1, Ref: a}, {KeyWidth: 8, Count: 1, Ref: a}}},
		"descending sections":   {Kind: KindCAR, Indexes: []Index{{KeyWidth: 8, Count: 1, Ref: SHA{2}}, {KeyWidth: 8, Count: 1, Ref: SHA{1}}}},
		"too many sections":     {Kind: KindCAR, Indexes: tooManySections()},
	}
	for name, r := range bad {
		if _, err := r.Marshal(); err == nil {
			t.Errorf("%s: marshalled", name)
		}
	}
}

func TestConcatRoundTripAndBoundedReads(t *testing.T) {
	st := NewMemStore()
	big := bytes.Repeat([]byte("0123456789abcdef"), 1<<14) // 256 KiB: streamed in several pieces
	id, _ := concatOf(st, []byte("head|"), big, []byte("|tail"))
	var out bytes.Buffer
	n, sum, err := (&Env{Src: st}).Reconstruct(id, &out)
	want := append(append([]byte("head|"), big...), "|tail"...)
	if err != nil || n != int64(len(want)) || !bytes.Equal(out.Bytes(), want) || sum != sha256.Sum256(want) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	// ReadObject refuses to allocate for an object over the cap, before reading it
	bid, _ := st.Put(big)
	st.ResetReads()
	if _, err := ReadObject(st, bid, -1, 1024); !errors.Is(err, ErrLimit) {
		t.Fatalf("cap: %v", err)
	}
	if reads, _ := st.Touched(); reads != 0 {
		t.Fatalf("%d reads before the cap check", reads)
	}
	if _, err := ReadObject(st, bid, int64(len(big))+1, 1<<30); !errors.Is(err, ErrMismatch) {
		t.Fatalf("declared length: %v", err)
	}
}

type shortSource struct{ *MemStore }

func (s shortSource) ReadAt(id SHA, p []byte, off int64) (int, error) {
	n, _ := s.MemStore.ReadAt(id, p, off)
	if n > 1 {
		n--
	}
	return n, nil // short, and no error
}

type failWriter struct{ left int }

func (w *failWriter) Write(p []byte) (int, error) {
	if len(p) > w.left {
		return 0, errors.New("disk full")
	}
	w.left -= len(p)
	return len(p), nil
}

func TestReconstructFailsCleanly(t *testing.T) {
	st := NewMemStore()
	comp := bytes.Repeat([]byte{7}, 1000)
	id, rep := concatOf(st, []byte("one"), comp)
	e := &Env{Src: st}
	cid := rep.Members[1].SHA

	if _, _, err := (&Env{Src: shortSource{st}}).Reconstruct(id, io.Discard); err == nil {
		t.Error("short source accepted")
	}
	if _, _, err := e.Reconstruct(id, &failWriter{left: 500}); err == nil || err.Error() == "" {
		t.Error("writer failure not reported")
	}
	for _, left := range []int{0, 2, 3, 10, 1002} { // every writer failure point
		if _, _, err := e.Reconstruct(id, &failWriter{left: left}); err == nil {
			t.Errorf("writer failing after %d bytes: no error", left)
		}
	}

	st.Set(cid, append(append([]byte(nil), comp...), 1)) // wrong length
	if _, _, err := e.Reconstruct(id, io.Discard); !errors.Is(err, ErrMismatch) {
		t.Errorf("wrong component length: %v", err)
	}
	wrong := append([]byte(nil), comp...)
	wrong[500] ^= 1
	st.Set(cid, wrong) // right length, wrong bytes
	if _, _, err := e.Reconstruct(id, io.Discard); !errors.Is(err, ErrMismatch) {
		t.Errorf("wrong component bytes: %v", err)
	}
	st.Delete(cid)
	if _, _, err := e.Reconstruct(id, io.Discard); !errors.Is(err, ErrMissing) {
		t.Errorf("missing component: %v", err)
	}
	// a corrupt representation object never reaches a decoder
	b := append([]byte(nil), st.Bytes(id)...)
	b[len(b)-3] ^= 1
	st.Set(id, b)
	if _, _, err := e.Reconstruct(id, io.Discard); !errors.Is(err, ErrMismatch) {
		t.Errorf("corrupt representation object: %v", err)
	}
	// a registered kind without a decoder is unsupported, never a guess
	k := &Rep{Kind: KindSQLite, Size: 0}
	kid := put(st, k)
	if _, _, err := e.Reconstruct(kid, io.Discard); !errors.Is(err, ErrUnsupported) {
		t.Errorf("kind without decoder: %v", err)
	}
}

func TestOutputBoundedByDeclaredSize(t *testing.T) {
	// a decoder that writes more than the representation declares is stopped
	st := NewMemStore()
	id := put(st, &Rep{Kind: KindCAR, Size: 4})
	e := &Env{Src: st, Table: func(Kind) (Decoder, bool) { return flood{}, true }}
	var out bytes.Buffer
	if _, _, err := e.Reconstruct(id, &out); !errors.Is(err, ErrMismatch) || out.Len() > 4 {
		t.Fatalf("err=%v wrote %d", err, out.Len())
	}
	e.Table = func(Kind) (Decoder, bool) { return short{}, true }
	if _, _, err := e.Reconstruct(id, io.Discard); !errors.Is(err, ErrMismatch) {
		t.Fatalf("short output: %v", err)
	}
}

type flood struct{}

func (flood) Decode(c *Call) error {
	for {
		if _, err := c.W.Write([]byte("0123456789")); err != nil {
			return err
		}
	}
}
func (flood) Expand(*Call, func(SHA)) error { return nil }

type short struct{}

func (short) Decode(c *Call) error          { _, err := c.W.Write([]byte("ab")); return err }
func (short) Expand(*Call, func(SHA)) error { return nil }

func TestDerivedMembers(t *testing.T) {
	st := NewMemStore()
	e := &Env{Src: st}
	childID, _ := concatOf(st, []byte("inner "), []byte("object"))
	framed := []byte("blob 12\x00inner object\x00\x00\x00\x00")
	f := Frame{Prefix: []byte("blob 12\x00"), Zeros: 4}
	id, rep := derive(st, childID, 12, f, framed)
	got, err := rebuild(e, id)
	if err != nil || !bytes.Equal(got, framed) {
		t.Fatalf("%q %v", got, err)
	}

	// reachability lists the parent, the child representation and the child's components
	r, err := e.Reachable(id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Count(DepRep) != 2 || r.Count(DepStored) != 2 || r.Derived != 1 || r.Role[childID] != DepRep {
		t.Fatalf("reach %+v", r)
	}
	if _, ok := r.Role[rep.Members[0].SHA]; ok {
		t.Fatal("a derived member's bytes are never a stored dependency")
	}
	// remove a child-only dependency: the walker had listed it, reconstruction fails cleanly
	var childOnly SHA
	for _, m := range mustLoad(t, e, childID).Members {
		childOnly = m.SHA
	}
	if r.Role[childOnly] != DepStored {
		t.Fatal("child component not listed")
	}
	st.Delete(childOnly)
	if _, err := rebuild(e, id); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing child component: %v", err)
	}

	// a wrong member digest and a wrong member length are caught after the child runs
	st2 := NewMemStore()
	e2 := &Env{Src: st2}
	c2, _ := concatOf(st2, []byte("inner object"))
	bad := &Rep{Kind: KindConcat, Size: int64(len(framed)), Members: []Member{{Role: Derived, SHA: sha256.Sum256([]byte("other")), Size: int64(len(framed)), Child: c2, Frame: f}}}
	if _, err := rebuild(e2, put(st2, bad)); !errors.Is(err, ErrMismatch) {
		t.Errorf("wrong child output hash: %v", err)
	}
	bad.Members[0].SHA = sha256.Sum256(framed)
	bad.Members[0].Size, bad.Size = int64(len(framed))+1, int64(len(framed))+1
	if _, err := rebuild(e2, put(st2, bad)); !errors.Is(err, ErrMismatch) {
		t.Errorf("wrong child output length: %v", err)
	}
	// framing that does not fit is rejected before any output
	bad.Members[0].Size, bad.Size = 14, 14
	bad.Members[0].Frame = Frame{Prefix: []byte("0123456789"), Zeros: 10}
	if _, err := bad.Marshal(); err == nil {
		t.Error("frame larger than the member marshalled")
	}
	// a missing child representation
	st2.Delete(c2)
	good := &Rep{Kind: KindConcat, Size: int64(len(framed)), Members: []Member{{Role: Derived, SHA: sha256.Sum256(framed), Size: int64(len(framed)), Child: c2, Frame: f}}}
	if _, err := rebuild(e2, put(st2, good)); !errors.Is(err, ErrMissing) {
		t.Errorf("missing child representation: %v", err)
	}
}

func mustLoad(t *testing.T, e *Env, id SHA) *Rep {
	t.Helper()
	r, err := e.LoadRep(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// nest builds depth levels of derived concat representations around one component.
func nest(st *MemStore, depth int) (SHA, []byte) {
	payload := []byte("leaf bytes")
	id, _ := concatOf(st, payload)
	for i := 1; i < depth; i++ {
		id, _ = derive(st, id, int64(len(payload)), Frame{}, payload)
	}
	return id, payload
}

func TestNestingDepth(t *testing.T) {
	st := NewMemStore()
	three, payload := nest(st, 3)
	four, _ := nest(st, 4)
	five, _ := nest(st, 5)
	e := &Env{Src: st}
	if got, err := rebuild(e, three); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("depth 3 (gzip -> tar -> database) by default: %v", err)
	}
	if _, err := rebuild(e, four); !errors.Is(err, ErrDepth) || !errors.Is(err, ErrLimit) {
		t.Fatalf("depth 4 by default: %v", err)
	}
	if _, err := e.Reachable(four); !errors.Is(err, ErrDepth) {
		t.Fatalf("walker depth 4: %v", err)
	}
	e4 := &Env{Src: st, Limits: Limits{MaxDepth: 4}}
	if got, err := rebuild(e4, four); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("depth 4 opted in: %v", err)
	}
	e9 := &Env{Src: st, Limits: Limits{MaxDepth: 99}} // clamped to the hard maximum
	if _, err := rebuild(e9, five); !errors.Is(err, ErrDepth) {
		t.Fatalf("depth 5 under MaxDepth 99: %v", err)
	}
}

func TestCycleAndSelfReference(t *testing.T) {
	st := NewMemStore()
	// A derived member naming its own representation: only constructible by lying
	// about the address, so the digest check is bypassed to reach the cycle guard.
	self := SHA{1, 2, 3}
	r := &Rep{Kind: KindConcat, Size: 3, Members: []Member{{Role: Derived, SHA: sha256.Sum256([]byte("abc")), Size: 3, Child: self}}}
	b, _ := r.Marshal()
	st.Set(self, b)
	e := &Env{Src: st, noVerify: true, Limits: Limits{MaxDepth: 4}}
	if _, err := rebuild(e, self); !errors.Is(err, ErrCycle) {
		t.Fatalf("self reference: %v", err)
	}
	if _, err := e.Reachable(self); !errors.Is(err, ErrCycle) {
		t.Fatalf("walker self reference: %v", err)
	}
	// a two-step cycle A -> B -> A
	a, bb := SHA{9}, SHA{8}
	ra := &Rep{Kind: KindConcat, Size: 3, Members: []Member{{Role: Derived, SHA: SHA{}, Size: 3, Child: bb}}}
	rb := &Rep{Kind: KindConcat, Size: 3, Members: []Member{{Role: Derived, SHA: SHA{}, Size: 3, Child: a}}}
	x, _ := ra.Marshal()
	y, _ := rb.Marshal()
	st.Set(a, x)
	st.Set(bb, y)
	if _, err := rebuild(e, a); !errors.Is(err, ErrCycle) {
		t.Fatalf("two-step cycle: %v", err)
	}
	// with the digest check on, a forged address is rejected as a mismatch
	if _, err := rebuild(&Env{Src: st}, self); !errors.Is(err, ErrMismatch) {
		t.Fatalf("forged address: %v", err)
	}
}

func TestSharedChildIsNotACycle(t *testing.T) {
	st := NewMemStore()
	child, _ := concatOf(st, []byte("shared"))
	f := []byte("shared")
	m := Member{Role: Derived, SHA: sha256.Sum256(f), Size: 6, Child: child}
	id := put(st, &Rep{Kind: KindConcat, Size: 12, Members: []Member{m, m}})
	e := &Env{Src: st}
	if got, err := rebuild(e, id); err != nil || string(got) != "sharedshared" {
		t.Fatalf("%q %v", got, err)
	}
	r, err := e.Reachable(id)
	if err != nil || r.Count(DepRep) != 2 || r.Derived != 2 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestObjectLimitOnWalk(t *testing.T) {
	st := NewMemStore()
	var parts [][]byte
	for i := 0; i < 50; i++ {
		parts = append(parts, []byte(fmt.Sprintf("part %d", i)))
	}
	id, _ := concatOf(st, parts...)
	if _, err := (&Env{Src: st, Limits: Limits{MaxObjects: 10}}).Reachable(id); !errors.Is(err, ErrLimit) {
		t.Fatalf("%v", err)
	}
	if _, err := (&Env{Src: st, Limits: Limits{MaxMembers: 10}}).Reachable(id); !errors.Is(err, ErrLimit) {
		t.Fatalf("member limit: %v", err)
	}
}

func TestNativeIndexLookup(t *testing.T) {
	st := NewMemStore()
	pairs := map[string]SHA{}
	for i := 0; i < 1000; i++ {
		k := fmt.Sprintf("key-%05d", i*3)
		pairs[k] = sha256.Sum256([]byte(k))
	}
	recs, n, err := BuildIndex(9, pairs)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := st.Put(recs)
	x := Index{KeyWidth: 9, Count: n, Ref: ref}
	for _, k := range []string{"key-00000", "key-00003", "key-02997"} {
		id, ok, err := LookupIndex(st, x, []byte(k))
		if err != nil || !ok || id != pairs[k] {
			t.Fatalf("%s: %v %v", k, ok, err)
		}
	}
	for _, k := range []string{"key-00001", "key-99999", "aaa-00000"} {
		if _, ok, err := LookupIndex(st, x, []byte(k)); ok || err != nil {
			t.Fatalf("%s found=%v err=%v", k, ok, err)
		}
	}
	st.ResetReads()
	LookupIndex(st, x, []byte("key-01500"))
	if reads, touched := st.Touched(); reads > 11 || touched[ref] > 11*41 {
		t.Fatalf("lookup read %d times, %d bytes (log2 1000 probes expected)", reads, touched[ref])
	}
	x.Count++ // the recipe claims more records than the component holds
	if _, _, err := LookupIndex(st, x, []byte("key-00000")); !errors.Is(err, ErrMismatch) {
		t.Fatalf("index length: %v", err)
	}
	if _, _, err := BuildIndex(3, map[string]SHA{"toolong": {}}); err == nil {
		t.Fatal("key width not enforced")
	}
}

func TestCostIsDerivedFromPersistedObjects(t *testing.T) {
	st := NewMemStore()
	e := &Env{Src: st}
	var added []SHA
	put1 := func(b []byte) SHA {
		id, isNew := st.Put(b)
		if isNew {
			added = append(added, id)
		}
		return id
	}
	_ = put1
	id, rep := concatOf(st, []byte("aaaa"), []byte("bb"))
	all := []SHA{rep.Members[0].SHA, rep.Members[1].SHA, id}
	c, err := e.Account(id, all)
	if err != nil || c.Payload != 6 || c.Objects != 3 || c.Recipe != int64(len(st.Bytes(id))) || c.Total(10) != c.Bytes()+30 {
		t.Fatalf("%+v %v", c, err)
	}
	stray, _ := st.Put([]byte("hidden"))
	if _, err := e.Account(id, append(all, stray)); err == nil {
		t.Fatal("an unreachable stored object was accepted as part of the cost")
	}
}

func FuzzUnmarshal(f *testing.F) {
	st := NewMemStore()
	good, _ := fullRep(st).Marshal()
	f.Add(good)
	f.Add([]byte(golden))
	b, _ := hex.DecodeString(golden)
	f.Add(b)
	f.Add([]byte("Z3RP\x02\x01\x00\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := Unmarshal(b, Limits{MaxMembers: 1 << 12, MaxRepBytes: 1 << 20})
		if err != nil {
			return
		}
		// anything that parses must re-marshal to the same bytes
		out, err := r.Marshal()
		if err != nil || !bytes.Equal(out, b) {
			t.Fatalf("parsed object does not round-trip: %v", err)
		}
	})
}

// TestReachabilityIsDepthSound: a child shared by two roots at different depths must be
// judged by its deepest use. Memoising a visited representation without its height would
// make Reachable([A, B]) succeed while B alone fails (and while Reconstruct(B) fails),
// so liveness would depend on which other roots happen to be in the set.
func TestReachabilityIsDepthSound(t *testing.T) {
	st := NewMemStore()
	leaf, _ := concatOf(st, []byte("leaf"))
	mid, _ := derive(st, leaf, 4, Frame{}, []byte("leaf"))    // height 2
	shallow, _ := derive(st, mid, 4, Frame{}, []byte("leaf")) // A: mid at depth 2, leaf at 3
	x, _ := derive(st, mid, 4, Frame{}, []byte("leaf"))
	y, _ := derive(st, x, 4, Frame{}, []byte("leaf")) // x at 2 ... use a chain so mid lands at depth 4
	z, _ := derive(st, y, 4, Frame{}, []byte("leaf"))
	deep, _ := derive(st, z, 4, Frame{}, []byte("leaf")) // mid at depth 5 under MaxDepth 4
	e := &Env{Src: st, Limits: Limits{MaxDepth: 4}}
	if _, err := rebuild(e, shallow); err != nil {
		t.Fatal(err)
	}
	if _, err := rebuild(e, deep); !errors.Is(err, ErrDepth) {
		t.Fatalf("reconstruct of the deep root: %v", err)
	}
	if _, err := e.ReachableAll([]SHA{deep}); !errors.Is(err, ErrDepth) {
		t.Fatalf("deep root alone: %v", err)
	}
	if _, err := e.ReachableAll([]SHA{shallow, deep}); !errors.Is(err, ErrDepth) {
		t.Fatalf("deep root after a shallow root that shares its child: %v", err)
	}
	if _, err := e.ReachableAll([]SHA{deep, shallow}); !errors.Is(err, ErrDepth) {
		t.Fatalf("deep root before a shallow root: %v", err)
	}
	if _, err := e.ReachableAll([]SHA{shallow}); err != nil {
		t.Fatalf("shallow root: %v", err)
	}
	// the native accelerator reads the same trees and must agree with reconstruction
	cat := NewCatalog(st, nil)
	cat.SetLimits(Limits{MaxDepth: 4})
	if err := cat.Ingest(deep); !errors.Is(err, ErrDepth) {
		t.Fatalf("catalog ingest of the deep root: %v", err)
	}
	if err := cat.Ingest(shallow); err != nil {
		t.Fatalf("catalog ingest of the shallow root: %v", err)
	}
	if err := NewCatalog(st, nil).Ingest(mustDepth4(st)); !errors.Is(err, ErrDepth) {
		t.Fatalf("a default-limit catalog must refuse a depth-4 root: %v", err)
	}
}

func mustDepth4(st *MemStore) SHA { id, _ := nest(st, 4); return id }

// TestLongerCycleAmongValidRepresentations: A -> B -> C -> A, where B and C are otherwise
// ordinary representations (equal sizes, so the cycle is the only inconsistency) next to a healthy
// root. Every walk returns ErrCycle; no
// partial output is accepted; the healthy sibling still reconstructs.
func TestLongerCycleAmongValidRepresentations(t *testing.T) {
	st := NewMemStore()
	healthy, _ := concatOf(st, []byte("fine"))
	a, b, c := SHA{0xa1}, SHA{0xb2}, SHA{0xc3}
	link := func(next SHA) []byte {
		r := &Rep{Kind: KindConcat, Size: 3, Members: []Member{{Role: Derived, SHA: SHA{}, Size: 3, Child: next}}}
		raw, err := r.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	st.Set(a, link(b))
	st.Set(b, link(c))
	st.Set(c, link(a))
	e := &Env{Src: st, noVerify: true, Limits: Limits{MaxDepth: 4}}
	for _, root := range []SHA{a, b, c} {
		var out bytes.Buffer
		if _, _, err := e.Reconstruct(root, &out); !errors.Is(err, ErrCycle) && !errors.Is(err, ErrDepth) {
			t.Fatalf("root %x: %v", root[:2], err)
		}
		if _, err := e.Reachable(root); !errors.Is(err, ErrCycle) {
			t.Fatalf("walker, root %x: %v", root[:2], err)
		}
		if _, err := e.ReachableAll([]SHA{healthy, root}); !errors.Is(err, ErrCycle) {
			t.Fatalf("walker with a healthy root first: %v", err)
		}
	}
	if got, err := rebuild(&Env{Src: st}, healthy); err != nil || string(got) != "fine" {
		t.Fatalf("healthy sibling: %q %v", got, err)
	}
	cat := NewCatalog(st, nil)
	cat.SetLimits(Limits{MaxDepth: 4})
	cat.env.noVerify = true
	if err := cat.Ingest(a); !errors.Is(err, ErrCycle) {
		t.Fatalf("catalog: %v", err)
	}
}
