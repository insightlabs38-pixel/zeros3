package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
	"zeros3-z3/lz4z3"
)

func init() { cmds["lz4frames"] = lz4frames }

// lz4frames FILE...: analyze each .lz4 frame stream, replay it in-process and verify bytes.
func lz4frames(args []string) error {
	sort.Strings(args)
	bad := 0
	for _, f := range args {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		t0 := time.Now()
		plain, rec, st, err := lz4z3.AnalyzeFrames(data, lz4z3.Limits{})
		if err != nil {
			fmt.Printf("%-34s UNSUPPORTED/ERR %v\n", filepath.Base(f), err)
			continue
		}
		ta := time.Since(t0)
		t0 = time.Now()
		var out bytes.Buffer
		err = lz4z3.ReplayFrames(&out, rec, bytes.NewReader(plain), lz4z3.Limits{})
		tr := time.Since(t0)
		ok := err == nil && bytes.Equal(out.Bytes(), data)
		if !ok {
			bad++
		}
		fmt.Printf("%-34s comp=%9d plain=%9d blocks=%5d pred=%5d hyb=%4d expl=%5d stored=%d linked=%d recipe=%7d (%.2f%%) analyze=%v replay=%v exact=%v sha=%x\n",
			filepath.Base(f), len(data), len(plain), st.Blocks, st.Predicted, st.Hybrid, st.Explicit, st.Stored, st.Linked, len(rec), 100*float64(len(rec))/float64(len(data)), ta.Round(time.Millisecond), tr.Round(time.Millisecond), ok, sha256.Sum256(data))
	}
	if bad > 0 {
		return fmt.Errorf("%d streams not reproduced", bad)
	}
	return nil
}
