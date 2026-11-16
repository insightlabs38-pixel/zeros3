package main

import (
	"encoding/binary"
	"fmt"
	"os"

	"zeros3-z3/parquetz3"
)

func init() { cmds["pqpages"] = cmdPqPages }

// pqpages FILE OUT: dumps every page payload of a Parquet file as records
// (u32 codec, u32 page type, u32 uncompressed size, u32 payload length, payload) for oracle probes.
func cmdPqPages(args []string) {
	f, err := os.Open(args[0])
	must(err)
	fi, _ := f.Stat()
	pf, err := parquetz3.Open(f, fi.Size(), parquetz3.Limits{})
	must(err)
	out, err := os.Create(args[1])
	must(err)
	defer out.Close()
	n := 0
	for ri := range pf.RowGroups {
		for ci := range pf.RowGroups[ri].Chunks {
			c := &pf.RowGroups[ri].Chunks[ci]
			must(pf.Pages(f, c, func(p *parquetz3.Page, hdr []byte) error {
				b := make([]byte, p.Comp)
				if _, err := f.ReadAt(b, p.PayloadOff()); err != nil {
					return err
				}
				var h [16]byte
				binary.LittleEndian.PutUint32(h[0:], uint32(c.Codec))
				binary.LittleEndian.PutUint32(h[4:], uint32(p.Type))
				binary.LittleEndian.PutUint32(h[8:], uint32(p.Uncomp))
				binary.LittleEndian.PutUint32(h[12:], uint32(len(b)))
				out.Write(h[:])
				out.Write(b)
				n++
				return nil
			}))
		}
	}
	fmt.Println("pages", n)
}
