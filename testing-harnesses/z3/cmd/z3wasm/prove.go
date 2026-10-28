package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"time"

	"zeros3-z3/econx"
	"zeros3-z3/wasmz3"
)

func init() { cmds["prove"] = prove }

// serialize flattens a representation to one byte string and parse reads it back: nothing but
// these bytes survives between encode and decode.
func serialize(meta []byte, comps [][]byte) []byte {
	out := binary.AppendUvarint(nil, uint64(len(meta)))
	out = append(out, meta...)
	out = binary.AppendUvarint(out, uint64(len(comps)))
	for _, c := range comps {
		out = binary.AppendUvarint(out, uint64(len(c)))
		out = append(out, c...)
	}
	return out
}

func parseRep(b []byte) ([]byte, [][]byte, error) {
	rd := func() ([]byte, error) {
		n, k := binary.Uvarint(b)
		if k <= 0 || n > uint64(len(b)-k) {
			return nil, fmt.Errorf("bad framing")
		}
		x := b[k : k+int(n)]
		b = b[k+int(n):]
		return x, nil
	}
	meta, err := rd()
	if err != nil {
		return nil, nil, err
	}
	n, k := binary.Uvarint(b)
	if k <= 0 {
		return nil, nil, fmt.Errorf("bad framing")
	}
	b = b[k:]
	var comps [][]byte
	for i := uint64(0); i < n; i++ {
		c, err := rd()
		if err != nil {
			return nil, nil, err
		}
		comps = append(comps, c)
	}
	return meta, comps, nil
}

// prove FILE... : encode, serialize, drop the analysis state, reload and decode; length, SHA-256
// and bytes are compared.
func prove(args []string) error {
	ok, fb := 0, 0
	var total int64
	t0 := time.Now()
	for _, p := range args {
		d, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dir, err := wasmz3.Parse(bytes.NewReader(d), int64(len(d)), wasmz3.Limits{})
		var m *wasmz3.Module
		if err == nil {
			m, err = wasmz3.Analyze(dir, d, wasmz3.Limits{})
		}
		var rep *wasmz3.Rep
		if err == nil {
			rep, err = wasmz3.Encode(m, d, wasmz3.Config{Auto: 12, Cut: econx.UnitCut(4).Cut})
		}
		if err != nil {
			fb++
			fmt.Printf("fallback %s: %v\n", p, err)
			continue
		}
		wire := serialize(rep.Meta, rep.Comps)
		rep, m, dir = nil, nil, nil
		meta, comps, err := parseRep(wire)
		if err != nil {
			return err
		}
		got, err := wasmz3.Decode(meta, comps)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if len(got) != len(d) || sha256.Sum256(got) != sha256.Sum256(d) || !bytes.Equal(got, d) {
			return fmt.Errorf("%s: rebuilt module differs", p)
		}
		ok++
		total += int64(len(d))
	}
	fmt.Printf("%d modules proved (%d MiB), %d kept as CDC, %v\n", ok, total>>20, fb, time.Since(t0).Round(time.Millisecond))
	return nil
}
