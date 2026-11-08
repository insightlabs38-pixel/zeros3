// z4p0merkle writes deterministic Z3SNAP1 Merkle-state snapshots (production repKindMerkle inputs)
// for P0: z4p0merkle OUT_PREFIX SEED KEYS [UPDATES] -> OUT_PREFIX-0.snap, OUT_PREFIX-1.snap
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"

	"zeros3-z3/merklez3"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: z4p0merkle OUT_PREFIX SEED KEYS [UPDATES]")
		os.Exit(2)
	}
	seed, _ := strconv.ParseUint(os.Args[2], 10, 64)
	n, _ := strconv.Atoi(os.Args[3])
	upd := n / 100
	if len(os.Args) > 4 {
		upd, _ = strconv.Atoi(os.Args[4])
	}
	st := merklez3.NewState(seed, n)
	cfg := merklez3.DefaultConfig()
	for v := 0; v < 2; v++ {
		ms := merklez3.NewMapStore()
		root, _ := st.BuildInto(cfg, ms)
		f, err := os.Create(fmt.Sprintf("%s-%d.snap", os.Args[1], v))
		if err != nil {
			panic(err)
		}
		w := bufio.NewWriter(f)
		if _, err := merklez3.Snapshot(ms, root, w); err != nil {
			panic(err)
		}
		w.Flush()
		f.Close()
		keys := make([]uint64, 0, upd)
		for i := 0; i < upd && i*97 < len(st.Keys); i++ {
			keys = append(keys, st.Keys[i*97])
		}
		st = st.Update(keys)
	}
}
