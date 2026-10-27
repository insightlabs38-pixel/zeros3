package main

import (
	"bytes"
	"fmt"
	"strings"

	"zeros3-z3/dexz3"
	"zeros3-z3/jvmz3"
	"zeros3-z3/pdelta"
)

func init() { cmds["attrib"] = attribCmd }

// Byte categories of the current file.
const (
	cContent = iota
	cIdx
	cOff
	cMeta  // debug / annotations / attribute metadata
	cFrame // header, map, container
)

type attribRes struct {
	Raw, Masked                       int // literal bytes raw / after masking all slots
	Lit, Idx, Off, Meta, Frame, Resid int
	Size                              int
}

func (a *attribRes) add(o attribRes) {
	a.Raw += o.Raw
	a.Masked += o.Masked
	a.Lit += o.Lit
	a.Idx += o.Idx
	a.Off += o.Off
	a.Meta += o.Meta
	a.Frame += o.Frame
	a.Resid += o.Resid
	a.Size += o.Size
}

func attribute(base, cur, baseM, curM []byte, cat []uint8) attribRes {
	ra := pdelta.Runs(base, cur, true).Runs
	rb := pdelta.Runs(baseM, curM, true).Runs
	mark := func(rs [][2]int) []bool {
		m := make([]bool, len(cur))
		for _, r := range rs {
			for i := r[0]; i < r[1] && i < len(m); i++ {
				m[i] = true
			}
		}
		return m
	}
	A, B := mark(ra), mark(rb)
	r := attribRes{Size: len(cur)}
	for i := range cur {
		if A[i] {
			r.Raw++
		}
		if B[i] {
			r.Masked++
			if cat[i] == cMeta {
				r.Meta++
			} else {
				r.Lit++
			}
			continue
		}
		if !A[i] {
			continue
		}
		switch cat[i] {
		case cIdx:
			r.Idx++
		case cOff:
			r.Off++
		case cFrame:
			r.Frame++
		case cMeta:
			r.Meta++
		default:
			r.Resid++
		}
	}
	return r
}

func dexCat(f *dexz3.File, w *dexz3.Walked, n int) []uint8 {
	cat := make([]uint8, n)
	meta := map[uint16]bool{dexz3.TypeDebugInfo: true, dexz3.TypeAnnotation: true, dexz3.TypeAnnoDir: true, dexz3.TypeAnnoSet: true, dexz3.TypeAnnoSetRef: true, dexz3.TypeEncodedArray: true}
	for _, sp := range f.Spans {
		c := uint8(cContent)
		switch {
		case meta[sp.Type]:
			c = cMeta
		case sp.Type == dexz3.TypeHeader || sp.Type == dexz3.TypeMapList || sp.Type == dexz3.TypeGap:
			c = cFrame
		}
		for i := sp.Off; i < sp.Off+sp.Len; i++ {
			cat[i] = c
		}
	}
	for _, s := range w.Slots {
		c := uint8(cIdx)
		if s.Space == dexz3.SpOffset {
			c = cOff
		}
		for i := s.Off; i < s.Off+int64(s.Len); i++ {
			cat[i] = c
		}
	}
	return cat
}

func printAttrib(title string, r attribRes) {
	p := func(v int) string { return fmt.Sprintf("%d (%.1f%%)", v, 100*float64(v)/float64(max(r.Raw, 1))) }
	fmt.Printf("%-34s raw-literal %9d (%.1f%% of %d) | literal %s | idx %s | offsets %s | debug/meta %s | container %s | residual %s\n",
		title, r.Raw, 100*float64(r.Raw)/float64(max(r.Size, 1)), r.Size, p(r.Lit), p(r.Idx), p(r.Off), p(r.Meta), p(r.Frame), p(r.Resid))
}

// attrib dex OLDDIR NEWDIR | attrib jvm OLDJAR NEWJAR
func attribCmd(args []string) error {
	switch args[0] {
	case "dex":
		old, err := loadRev(args[1])
		if err != nil {
			return err
		}
		cur, err := loadRev(args[2])
		if err != nil {
			return err
		}
		var base, baseM []byte
		for i, b := range old.Raw {
			base = append(base, b...)
			m := append([]byte(nil), b...)
			dexz3.Mask(m, old.W[i].Slots, nil)
			baseM = append(baseM, m...)
		}
		var tot attribRes
		for i, b := range cur.Raw {
			m := append([]byte(nil), b...)
			dexz3.Mask(m, cur.W[i].Slots, nil)
			tot.add(attribute(base, b, baseM, m, dexCat(cur.F[i], cur.W[i], len(b))))
		}
		printAttrib(fmt.Sprintf("DEX %s->%s", old.Name, cur.Name), tot)
	case "jvm":
		old, err := loadJvmSet(args[1])
		if err != nil {
			return err
		}
		cur, err := loadJvmSet(args[2])
		if err != nil {
			return err
		}
		var tot attribRes
		for i := range cur.raw {
			b := cur.raw[i]
			j, ok := old.idx[cur.names[i]]
			var base, baseM []byte
			if ok {
				base, baseM = old.raw[j], old.masked(j, nil)
			}
			m := cur.masked(i, nil)
			c, _, err := jvmz3.Parse(bytes.NewReader(b), int64(len(b)), jvmz3.Limits{})
			cat := make([]uint8, len(b))
			if err == nil {
				for _, r := range c.Ranges {
					k := uint8(cContent)
					switch r.Name {
					case "LineNumberTable", "LocalVariableTable", "LocalVariableTypeTable", "SourceFile", "Signature", "MethodParameters", "InnerClasses", "StackMapTable", "EnclosingMethod", "Deprecated":
						k = cMeta
					default:
						if strings.HasPrefix(r.Name, "Runtime") {
							k = cMeta
						}
					}
					if k == cMeta {
						for x := r.Off; x < r.End; x++ {
							cat[x] = cMeta
						}
					}
				}
				for _, s := range c.Slots {
					for x := s.Off; x < s.Off+int64(s.Len); x++ {
						cat[x] = cIdx
					}
				}
			}
			if !ok {
				base, baseM = nil, nil
				// an added class is all literal content
				tot.add(attribRes{Raw: len(b), Masked: len(b), Lit: len(b), Size: len(b)})
				continue
			}
			tot.add(attribute(base, b, baseM, m, cat))
		}
		printAttrib(fmt.Sprintf("JVM %s", args[2][max(0, len(args[2])-12):]), tot)
	}
	return nil
}
