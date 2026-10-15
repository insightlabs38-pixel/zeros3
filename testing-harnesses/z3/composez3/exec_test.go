package composez3

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	"zeros3-z3/lab"
)

func smallELF(t testing.TB) []byte {
	for _, p := range []string{"/bin/true", "/usr/bin/true", "/bin/ls", "/usr/bin/env"} {
		if b, err := os.ReadFile(p); err == nil && bytes.HasPrefix(b, []byte("\x7fELF")) && len(b) > 4096 {
			return b
		}
	}
	t.Skip("no small ELF on this host")
	return nil
}

func storeOf(in *Inner) map[lab.ID][]byte {
	st := map[lab.ID][]byte{}
	for _, c := range in.Comps {
		b, _ := c.Data()
		st[c.ID] = b
	}
	return st
}

func TestExecRecipeHostile(t *testing.T) {
	exe := smallELF(t)
	in, err := Encode(KindExec, exe)
	if err != nil {
		t.Skip("host ELF not supported by the Z3 parser:", err)
	}
	st := storeOf(in)
	get := func(id lab.ID) ([]byte, error) {
		if b, ok := st[id]; ok {
			return b, nil
		}
		return nil, errors.New("missing")
	}
	var ok bytes.Buffer
	if err := rebuildExec(&ok, in.Recipe, get); err != nil || !bytes.Equal(ok.Bytes(), exe) {
		t.Fatal("round trip", err)
	}
	for n := 0; n < len(in.Recipe); n++ {
		if err := rebuildExec(io.Discard, in.Recipe[:n], get); err == nil {
			t.Fatalf("truncation at %d accepted", n)
		}
	}
	for i := range in.Recipe {
		m := append([]byte(nil), in.Recipe...)
		m[i] ^= 0x44
		// a mutation may be harmless (an unused region index) or change the output; the outer
		// digest check in Compose rejects the latter. Here: never a panic, bounded output.
		var out bytes.Buffer
		if err := rebuildExec(&out, m, get); err == nil && out.Len() > 4*len(exe)+(1<<20) {
			t.Fatalf("mutation at %d produced %d bytes", i, out.Len())
		}
	}
	// hostile counts: a stream directory claiming gigabytes allocates nothing
	h := append([]byte("EXR1"), 1, 2, 0, 0)                                   // one payload stream, chunked
	h = append(h, 0xff, 0xff, 0xff, 0xff, 0x0f, 0xff, 0xff, 0xff, 0xff, 0x0f) // length 4 GiB-ish, chunk count huge
	if err := rebuildExec(io.Discard, h, get); err == nil {
		t.Fatal("hostile directory accepted")
	}
}

func FuzzRebuildExec(f *testing.F) {
	exe := smallELF(f)
	in, err := Encode(KindExec, exe)
	if err != nil {
		f.Skip()
	}
	st := storeOf(in)
	get := func(id lab.ID) ([]byte, error) {
		if b, ok := st[id]; ok {
			return b, nil
		}
		return nil, errors.New("missing")
	}
	f.Add(in.Recipe)
	f.Fuzz(func(t *testing.T, b []byte) {
		var out bytes.Buffer
		if err := rebuildExec(&out, b, get); err == nil && out.Len() > 64<<20 {
			t.Fatalf("unbounded output %d", out.Len())
		}
	})
}
