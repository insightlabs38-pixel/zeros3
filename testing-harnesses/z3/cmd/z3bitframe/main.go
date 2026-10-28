// Native-oracle comparison and diagnostic hashes; no hashes supply recipe bytes.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"zeros3-z3/bitcodez3"
)

type function struct {
	ByteSHA, OperandSHA, ShapeSHA string
	Bytes, Records                int
}

func main() {
	b, e := os.ReadFile(os.Args[1])
	if e != nil {
		panic(e)
	}
	p, e := bitcodez3.Parse(b)
	if e != nil {
		panic(e)
	}
	blocks, records := map[uint64]int{}, map[uint64]int{}
	for _, v := range p.Blocks {
		blocks[v.ID]++
	}
	for _, v := range p.Records {
		records[v.Block]++
	}
	var functions []function
	// Record and block arrays are in source order; each record is visited once.
	cursor := 0
	for _, v := range p.Blocks {
		if v.ID != 12 {
			continue
		}
		for cursor < len(p.Records) && p.Records[cursor].Start < v.Body {
			cursor++
		}
		shape, values := sha256.New(), sha256.New()
		count := 0
		var scratch [24]byte
		for cursor < len(p.Records) && p.Records[cursor].End <= v.End {
			r := p.Records[cursor]
			binary.LittleEndian.PutUint64(scratch[:], r.Block)
			binary.LittleEndian.PutUint64(scratch[8:], r.Code)
			binary.LittleEndian.PutUint64(scratch[16:], uint64(r.Values))
			shape.Write(scratch[:])
			values.Write(scratch[:])
			values.Write([]byte(r.OperandSHA))
			count++
			cursor++
		}
		functions = append(functions, function{ByteSHA: fmt.Sprintf("%x", sha256.Sum256(b[v.Body/8:v.End/8])), OperandSHA: fmt.Sprintf("%x", values.Sum(nil)), ShapeSHA: fmt.Sprintf("%x", shape.Sum(nil)), Bytes: int((v.End - v.Body) / 8), Records: count})
	}
	out := struct {
		Blocks, Records map[uint64]int
		Abbrevs         int
		Functions       []function
	}{blocks, records, p.Abbrevs, functions}
	if e = json.NewEncoder(os.Stdout).Encode(out); e != nil {
		panic(e)
	}
}
