package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/dexz3"
	"zeros3-z3/jvmz3"
)

func init() { cmds["coverage"] = coverageCmd }

type cov struct {
	files, classes, methods, bytes, fb, fbBytes int
}

func (c *cov) dex(b []byte) {
	f, err := dexz3.Parse(bytes.NewReader(b), int64(len(b)), dexz3.Limits{})
	if err == nil {
		var rep *dexz3.Rep
		if rep, err = dexz3.Encode(f, b, dexz3.Config{Cut: unitCut(4).Cut}); err == nil {
			out, derr := dexz3.Decode(rep.Meta, rep.Comps)
			if derr != nil || !bytes.Equal(out, b) {
				panic("DEX round trip failed after a successful encode")
			}
			w, _ := dexz3.Walk(f, b, dexz3.WalkOpts{})
			c.files++
			c.classes += int(f.Hdr.ClassDefs)
			for _, it := range w.Items {
				if it.Type == dexz3.TypeCode {
					c.methods++
				}
			}
			c.bytes += len(b)
			return
		}
	}
	c.fb++
	c.fbBytes += len(b)
}

func (c *cov) class(b []byte) {
	cf, _, err := jvmz3.Parse(bytes.NewReader(b), int64(len(b)), jvmz3.Limits{})
	if err == nil {
		var rep *jvmz3.Rep
		if rep, err = jvmz3.Encode(cf, b, jvmz3.Config{}); err == nil {
			out, derr := jvmz3.Decode(rep.Meta, rep.Comps)
			if derr != nil || !bytes.Equal(out, b) {
				panic("class round trip failed after a successful encode")
			}
			c.files++
			c.classes++
			c.methods += cf.Methods
			c.bytes += len(b)
			return
		}
	}
	c.fb++
	c.fbBytes += len(b)
}

// coverage PATH...: exact round trip of every DEX and class file inside the given files.
func coverageCmd(args []string) error {
	for _, p := range args {
		var d, k cov
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		switch {
		case strings.HasSuffix(p, ".dex"):
			d.dex(b)
		case strings.HasSuffix(p, ".class"):
			k.class(b)
		default:
			zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
			if err != nil {
				return err
			}
			for _, f := range zr.File {
				isDex := strings.HasSuffix(f.Name, ".dex")
				isCls := strings.HasSuffix(f.Name, ".class")
				if !isDex && !isCls {
					continue
				}
				rc, err := f.Open()
				if err != nil {
					return err
				}
				m, err := io.ReadAll(io.LimitReader(rc, 256<<20))
				rc.Close()
				if err != nil {
					return err
				}
				if isDex {
					d.dex(m)
				} else {
					k.class(m)
				}
			}
		}
		fmt.Printf("%-52s", filepath.Base(filepath.Dir(p))+"/"+filepath.Base(p))
		if d.files+d.fb > 0 {
			fmt.Printf(" DEX: %d files %d classes %d methods %s exact, fallback %d", d.files, d.classes, d.methods, kib(int64(d.bytes)), d.fb)
		}
		if k.files+k.fb > 0 {
			fmt.Printf(" CLASS: %d files %d methods %s exact, fallback %d", k.files, k.methods, kib(int64(k.bytes)), k.fb)
		}
		fmt.Println()
	}
	return nil
}
