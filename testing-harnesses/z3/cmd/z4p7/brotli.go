package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	"zeros3-z3/brotliz3"
	_ "zeros3-z3/brotliz3/brdict"
)

func init() { cmds["brenc"] = cmdBrEnc }

// brenc -q Q -w LGWIN IN OUT: reference-exact Brotli encoding of IN.
// brenc -pages FILE: for a pqpages dump, re-encode each decoded page (decoded by the oracle into
// FILE.dec with the same framing) and count byte-identical payloads.
func cmdBrEnc(args []string) {
	fl := flag.NewFlagSet("brenc", flag.ExitOnError)
	q := fl.Int("q", 8, "quality")
	w := fl.Int("w", 22, "lgwin")
	pages := fl.String("pages", "", "page dump with decoded bodies (u32 len, body, u32 len, payload)*")
	fl.Parse(args)
	if *pages != "" {
		b, err := os.ReadFile(*pages)
		must(err)
		n, hit, bytesIn, bytesHit := 0, 0, 0, 0
		t0 := time.Now()
		for p := 0; p < len(b); {
			bl := int(binary.LittleEndian.Uint32(b[p:]))
			body := b[p+4 : p+4+bl]
			p += 4 + bl
			pl := int(binary.LittleEndian.Uint32(b[p:]))
			pay := b[p+4 : p+4+pl]
			p += 4 + pl
			out, err := brotliz3.Encode(body, *q, *w)
			n++
			bytesIn += bl
			if err == nil && bytes.Equal(out, pay) {
				hit++
				bytesHit += pl
			} else if n-hit <= 5 {
				fmt.Printf("miss page %d body %d payload %d got %d err %v\n", n, bl, pl, len(out), err)
			}
		}
		s := time.Since(t0).Seconds()
		fmt.Printf("pages %d exact %d payload-bytes-exact %d decoded %d  %.1f MiB/s\n", n, hit, bytesHit, bytesIn, float64(bytesIn)/1048576/s)
		return
	}
	in, err := os.ReadFile(fl.Arg(0))
	must(err)
	out, err := brotliz3.Encode(in, *q, *w)
	must(err)
	must(os.WriteFile(fl.Arg(1), out, 0o644))
}
