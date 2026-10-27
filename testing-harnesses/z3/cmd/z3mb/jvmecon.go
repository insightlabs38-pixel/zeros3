package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"zeros3-z3/jvmz3"
	"zeros3-z3/lab"
)

func init() { cmds["jvmecon"] = jvmEcon }

// classBundle prices all class files of a jar as standalone objects (no ZIP, no DEFLATE): one plan
// per class, merged into one revision plan so the index nodes cover the whole set.
func classBundle(name string, one func(f lab.EconFile) (*lab.Plan, bool, error)) func(f lab.EconFile) (*lab.Plan, bool, error) {
	return func(f lab.EconFile) (*lab.Plan, bool, error) {
		// f.Data holds the concatenation; its first bytes list the class boundaries (name:len;...)
		hdrEnd := bytes.IndexByte(f.Data, 0)
		parts := strings.Split(string(f.Data[:hdrEnd]), ";")
		body := f.Data[hdrEnd+1:]
		merged := &lab.Plan{}
		head := append([]byte(nil), f.Data[:hdrEnd+1]...)
		merged.Pieces = append(merged.Pieces, lab.Piece{Inline: head})
		var rebuilds []func(io.Writer, func(lab.ID) ([]byte, error)) error
		fb := false
		for _, p := range parts {
			if p == "" {
				continue
			}
			var n string
			var l int
			fmt.Sscanf(p[strings.LastIndexByte(p, ':')+1:], "%d", &l)
			n = p[:strings.LastIndexByte(p, ':')]
			cls := body[:l]
			body = body[l:]
			pl, f2, err := one(lab.EconFile{Name: n, Data: cls})
			if err != nil {
				return nil, false, err
			}
			fb = fb || f2
			merged.Pieces = append(merged.Pieces, pl.Pieces...)
			merged.MetaBytes += pl.MetaBytes
			rebuilds = append(rebuilds, pl.Rebuild)
		}
		merged.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
			if _, err := w.Write(head); err != nil {
				return err
			}
			for _, r := range rebuilds {
				if err := r(w, get); err != nil {
					return err
				}
			}
			return nil
		}
		return merged, fb, nil
	}
}

func jvmPlan(cfg jvmz3.Config) func(f lab.EconFile) (*lab.Plan, bool, error) {
	return func(f lab.EconFile) (*lab.Plan, bool, error) {
		c, _, err := jvmz3.Parse(bytes.NewReader(f.Data), int64(len(f.Data)), jvmz3.Limits{})
		if err == nil {
			var rep *jvmz3.Rep
			if rep, err = jvmz3.Encode(c, f.Data, cfg); err == nil {
				pieces := packPieces(rep.Meta, rep.Comps, "jvmn")
				p := &lab.Plan{Pieces: pieces}
				p.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
					meta, cs, err := unpackPieces(pieces, get)
					if err != nil {
						return err
					}
					out, err := jvmz3.Decode(meta, cs)
					if err != nil {
						return err
					}
					_, err = w.Write(out)
					return err
				}
				return p, false, nil
			}
		}
		pl, e := lab.CDCPlan(f, "class")
		return pl, true, e
	}
}

func jvmEcon(args []string) error {
	var names []string
	if args[0] != "all" {
		names = strings.Split(args[0], ",")
	}
	all := []econRep{
		{"cdc", func(f lab.EconFile) (*lab.Plan, bool, error) { p, e := lab.CDCPlan(f, "class"); return p, false, e }},
		{"n-whole", jvmPlan(jvmz3.Config{})},
		{"n-cp+body", jvmPlan(jvmz3.Config{Cut: cpBody})},
		{"n-unit-2k", jvmPlan(jvmz3.Config{Cut: unitCut(2).Cut})},
		{"n-unit-1k", jvmPlan(jvmz3.Config{Cut: unitCutB(1024).Cut})},
	}
	var reps []econRep
	for _, r := range all {
		if len(names) == 0 {
			reps = append(reps, r)
		}
		for _, n := range names {
			if n == r.Name {
				reps = append(reps, r)
			}
		}
	}
	for i := range reps {
		reps[i].Plan = classBundle(reps[i].Name, reps[i].Plan)
	}
	var revs []revFiles
	for i, p := range args[1:] {
		ns, data, err := jarClasses(p)
		if err != nil {
			return err
		}
		var hdr strings.Builder
		var body []byte
		for j := range ns {
			fmt.Fprintf(&hdr, "%s:%d;", ns[j], len(data[j]))
			body = append(body, data[j]...)
		}
		blob := append(append([]byte(hdr.String()), 0), body...)
		revs = append(revs, revFiles{fmt.Sprintf("r%d", i), []lab.EconFile{{Name: "classes", Data: blob}}})
	}
	return runFamily("standalone classes of "+strings.Join(args[1:], " "), revs, reps)
}
