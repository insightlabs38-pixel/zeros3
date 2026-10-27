package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"time"

	"zeros3-z3/lab"
)

func init() { cmds["zipperf"] = zipPerf }

// zipperf REP FILE: analysis time, whole-container rebuild time and this process's peak RSS.
func zipPerf(args []string) error {
	st := &innerStats{}
	reps := containerReps([]string{args[0]}, st)
	if len(reps) != 1 {
		return fmt.Errorf("unknown representation %s", args[0])
	}
	b, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	t0 := time.Now()
	pl, fb, err := reps[0].Plan(lab.EconFile{Name: "pkg", Data: b})
	if err != nil {
		return err
	}
	tAn := time.Since(t0)
	ac := lab.NewAcct(refBytes, true)
	if _, err := ac.Add(treeify(pl, treeFan)); err != nil {
		return err
	}
	t1 := time.Now()
	var out bytes.Buffer
	if err := pl.Rebuild(&out, ac.Get); err != nil {
		return err
	}
	tRe := time.Since(t1)
	if sha256.Sum256(out.Bytes()) != sha256.Sum256(b) {
		return fmt.Errorf("rebuild differs")
	}
	mb := float64(len(b)) / (1 << 20)
	fmt.Printf("%s %s: %.1f MiB fallback=%v | analyse %v (%.1f MiB/s) | rebuild %v (%.1f MiB/s) | peak RSS %s\n", args[0], args[1], mb, fb, tAn, mb/tAn.Seconds(), tRe, mb/tRe.Seconds(), vmhwm())
	return nil
}
