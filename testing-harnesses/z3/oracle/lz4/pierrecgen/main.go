// pierrecgen is a producer oracle outside the Z3 module: it compresses inputs with
// github.com/pierrec/lz4/v4 (CompressBlock) so lz4z3.PredPierrec can be checked against a real,
// independent Go implementation. usage: pierrecgen IN OUT  (OUT = raw LZ4 block)
//        pierrecgen -split N IN OUTDIR  (one block per N-byte chunk: OUTDIR/<i>.raw + <i>.lz4)
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/pierrec/lz4/v4"
)

func comp(in []byte) []byte {
	out := make([]byte, lz4.CompressBlockBound(len(in)))
	n, err := lz4.CompressBlock(in, out, nil)
	if err != nil {
		panic(err)
	}
	return out[:n]
}

func main() {
	if os.Args[1] == "-split" {
		n, _ := strconv.Atoi(os.Args[2])
		in, err := os.ReadFile(os.Args[3])
		if err != nil {
			panic(err)
		}
		os.MkdirAll(os.Args[4], 0o755)
		for i := 0; i*n < len(in); i++ {
			chunk := in[i*n : min((i+1)*n, len(in))]
			os.WriteFile(filepath.Join(os.Args[4], fmt.Sprintf("%05d.raw", i)), chunk, 0o644)
			os.WriteFile(filepath.Join(os.Args[4], fmt.Sprintf("%05d.lz4", i)), comp(chunk), 0o644)
		}
		return
	}
	in, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	os.WriteFile(os.Args[2], comp(in), 0o644)
}
