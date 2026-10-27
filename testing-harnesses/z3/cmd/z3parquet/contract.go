package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"

	"zeros3-z3/lab"
	"zeros3-z3/parquetz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// cmdContract stores the revision chain through the real repz3 contract (KindParquet, 35 B
// members, one representation object per file) into a MemStore, rebuilds every revision
// through the serialized form only, checks the reachability gate, and prices what was added.
func cmdContract(args []string) error {
	sts, _, err := loadFamily("pyarrow-chain")
	if err != nil {
		return err
	}
	st := repz3.NewMemStore()
	var cum, objects, repBytes int64
	fmt.Println("| revision | objects added | packed bytes added | representation object |")
	fmt.Println("|---|---:|---:|---:|")
	for _, s := range sts {
		data, err := readFile(s.Path)
		if err != nil {
			return err
		}
		b, _, _, err := kinds.EncodeParquet(bytes.NewReader(data), int64(len(data)), parquetz3.Options{})
		if err != nil {
			return err
		}
		base := st.Clone()
		if err := kinds.Prove(base, b, data); err != nil {
			return fmt.Errorf("%s: %v", s.Rev, err)
		}
		id, added, err := b.Persist(st)
		if err != nil {
			return err
		}
		var packed int64
		for _, a := range added {
			packed += lab.PackedSize(st.Bytes(a))
		}
		var out bytes.Buffer
		if _, sum, err := kinds.NewEnv(st).Reconstruct(id, &out); err != nil || sum != sha256.Sum256(data) {
			return fmt.Errorf("%s: reconstruct: %v", s.Rev, err)
		}
		cum += packed
		objects += int64(len(added))
		repBytes += int64(len(b.Raw))
		fmt.Printf("| %s | %d | %s | %s |\n", s.Rev, len(added), human(packed), human(int64(len(b.Raw))))
	}
	fmt.Printf("cumulative retained through the real contract: %s in %d objects (representation objects %s)\n", human(cum), objects, human(repBytes))
	return nil
}
