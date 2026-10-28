package main

import (
	"os"
	"path/filepath"
	"testing"

	"zeros3-z3/wasmz3"
)

// A small hand-assembled module (calls, a global, a data segment) for the command smoke test.
func fixtureModule() []byte {
	hdr := []byte("\x00asm\x01\x00\x00\x00")
	typ := []byte{1, 5, 1, 0x60, 1, 0x7f, 0}                 // type 0: (i32) -> ()
	imp := []byte{2, 9, 1, 3, 'e', 'n', 'v', 1, 'f', 0, 0}   // import env.f : type 0
	fn := []byte{3, 3, 2, 0, 0}                              // two functions of type 0
	call := []byte{0x20, 0, 0x10, 0, 0x0b}                   // local.get 0; call 0; end
	bodyA := append([]byte{byte(len(call) + 1), 0}, call...) // no locals
	bodyB := append([]byte{byte(len(call) + 1), 0}, []byte{0x20, 0, 0x10, 1, 0x0b}...)
	code := append([]byte{10, byte(1 + len(bodyA) + len(bodyB)), 2}, append(bodyA, bodyB...)...)
	return append(append(append(append(hdr, typ...), imp...), fn...), code...)
}

func TestCommandsOnModule(t *testing.T) {
	d := t.TempDir()
	good := filepath.Join(d, "m.wasm")
	if err := os.WriteFile(good, fixtureModule(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(good); err != nil {
		t.Fatal(err)
	}
	// the module must at least parse with the package under test before the commands run on it
	dir, err := wasmz3.Parse(readerAt(fixtureModule()), int64(len(fixtureModule())), wasmz3.Limits{})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if m, err := wasmz3.Analyze(dir, fixtureModule(), wasmz3.Limits{}); err != nil || len(m.Slots) != 5 {
		t.Fatalf("fixture: %v (%d slots)", err, len(m.Slots))
	}
	for name, args := range map[string][]string{
		"scan":   {good},
		"attrib": {good, good},
		"econ":   {"cdc,sections,funcs,raw-unit-4k,n-unit-4k-a12", good, good},
		"cover":  {good},
		"prove":  {good},
		"perf":   {good},
		"reads":  {good},
	} {
		if err := cmds[name](args); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, os.ErrClosed
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, os.ErrClosed
	}
	return n, nil
}

func readerAt(b []byte) bytesReaderAt { return bytesReaderAt(b) }
