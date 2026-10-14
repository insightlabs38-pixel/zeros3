package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"zeros3-z3/carz3"
	"zeros3-z3/lab"
)

func cmdReal(args []string) error {
	fs := flag.NewFlagSet("real", flag.ExitOnError)
	fs.Parse(args)
	names := []string{"sample-v1.car", "sample-wrapped-v2.car", "sample-v2-indexless.car"}
	fam := carz3.Family{Name: "real-go-car", Note: "ipld/go-car v2/testdata samples (one CARv1, its CARv2 wrap with index, the indexless CARv2)"}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(cacheDir(), n))
		if err != nil {
			return fmt.Errorf("%v (run `z3car fetch`)", err)
		}
		fam.Revs = append(fam.Revs, carz3.Rev{Label: n, Data: b})
	}
	printFamily(fam, true)
	// Admission: the two malformed samples must fall back, never mis-parse.
	fmt.Printf("\n#### admission of malformed real samples\n\n| sample | parser verdict | car-auto path | exact |\n|---|---|---|---|\n")
	for _, n := range []string{"sample-v1-with-zero-len-section.car", "sample-v1-tailing-corrupt-section.car"} {
		b, err := os.ReadFile(filepath.Join(cacheDir(), n))
		if err != nil {
			return err
		}
		_, _, perr := carz3.Build(bytes.NewReader(b), int64(len(b)), carz3.DefaultOptions())
		var m *carz3.MalformedError
		var u *carz3.UnsupportedError
		verdict := "accepted"
		switch {
		case errors.As(perr, &m):
			verdict = "malformed: " + m.Msg
		case errors.As(perr, &u):
			verdict = "unsupported: " + u.Msg
		case perr != nil:
			verdict = perr.Error()
		}
		rows := lab.RunBytesFamily(n, "car", []lab.NamedBytes{{Label: n, Data: b}}, lab.CARReps())
		for _, r := range rows {
			if r.Rep == "car-auto" {
				fmt.Printf("| %s | %s | %v | %v |\n", n, verdict, r.Detail["auto"], r.Exact)
			}
		}
	}
	return nil
}
