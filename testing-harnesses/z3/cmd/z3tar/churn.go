package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sort"

	"zeros3-z3/lab"
	"zeros3-z3/tarz3"
)

type span struct {
	a, b  int64
	class byte // 's' structure (headers, padding, trailer), 'u' payload of a member whose content exists in the base, 'c' payload of a new member
}

// regions tiles the plaintext of cur by structure / unchanged-payload / changed-payload.
func regions(cur, base *rev) []span {
	have := base.payloadDigests()
	var out []span
	pos := int64(0)
	for i := range cur.ar.Members {
		m := &cur.ar.Members[i]
		if m.PayOff > pos {
			out = append(out, span{pos, m.PayOff, 's'})
		}
		if m.PayLen > 0 {
			c := byte('c')
			if _, ok := have[sha256.Sum256(cur.plain[m.PayOff:m.PayOff+m.PayLen])]; ok {
				c = 'u'
			}
			out = append(out, span{m.PayOff, m.PayOff + m.PayLen, c})
		}
		pos = m.PayOff + m.PayLen
	}
	out = append(out, span{pos, int64(len(cur.plain)), 's'}) // padding of the last member, trailer
	return out
}

// header field offsets within a 512-byte block (checksum excluded from comparisons).
var fields = []struct {
	name string
	a, b int
}{{"name", 0, 100}, {"mode", 100, 108}, {"uid", 108, 116}, {"gid", 116, 124}, {"size", 124, 136}, {"mtime", 136, 148}, {"type+link", 156, 257}, {"magic+owner+rest", 257, 512}}

func headerBlock(r *rev, m *tarz3.Member) []byte { return r.plain[m.PayOff-tarz3.BlockSize : m.PayOff] }

func runChurn(revs map[string][]*rev, order []string) {
	type agg struct{ p, cdcNew, st, un, ch, memNew, flS, model, exact int64 }
	tot := map[string]*agg{}
	fmt.Println("Plaintext CDC floor of each update revision against its predecessor, and what the new bytes are (percent of the plaintext tar):")
	fmt.Println()
	fmt.Println("| family | pair | tar MiB | plaintext CDC new % | in structure % | in payload of members that exist in the base % | in payload of new members % | member-level new payload % | members | members with new payload | headers same / mtime-only / other-field / new path |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|")
	type hrow struct {
		fam, pair                string
		p, exact, flS, model, nm int64
		same, mt, other, fresh   int
	}
	var hrows []hrow
	for _, f := range order {
		list := revs[f]
		for j := 1; j < len(list); j++ {
			prev, cur := list[j-1], list[j]
			if prev.ar == nil || cur.ar == nil {
				fmt.Printf("| %s | %s -> %s | - | unsupported tar (%v) |||||||||\n", short(f), prev.label, cur.label, firstErr(prev.perr, cur.perr))
				continue
			}
			P := int64(len(cur.plain))
			// CDC floor and attribution
			have := prev.cdcChunks()
			rg := regions(cur, prev)
			var cdcNew, inS, inU, inC int64
			lab.ChunkCDC(bytes.NewReader(cur.plain), func(off int64, c []byte) error {
				if _, ok := have[sha256.Sum256(c)]; ok {
					return nil
				}
				cdcNew += int64(len(c))
				a, b := off, off+int64(len(c))
				k := sort.Search(len(rg), func(i int) bool { return rg[i].b > a })
				for ; k < len(rg) && rg[k].a < b; k++ {
					lo, hi := max(a, rg[k].a), min(b, rg[k].b)
					switch rg[k].class {
					case 's':
						inS += hi - lo
					case 'u':
						inU += hi - lo
					default:
						inC += hi - lo
					}
				}
				return nil
			})
			// member-level new payload bytes (distinct new digests)
			pd := prev.payloadDigests()
			var newPay int64
			seen := map[[32]byte]bool{}
			newMem, payMem := 0, 0
			for i := range cur.ar.Members {
				m := &cur.ar.Members[i]
				if m.PayLen == 0 {
					continue
				}
				payMem++
				d := sha256.Sum256(cur.plain[m.PayOff : m.PayOff+m.PayLen])
				if _, ok := pd[d]; !ok {
					newMem++
					if !seen[d] {
						seen[d] = true
						newPay += m.PayLen
					}
				}
			}
			// header comparison by path+type
			old := map[string][]byte{}
			for i := range prev.ar.Members {
				m := &prev.ar.Members[i]
				old[string(rune(m.Type))+"\x00"+m.Path] = headerBlock(prev, m)
			}
			var same, mt, other, fresh int
			var model int64
			var newHdrs []byte
			for i := range cur.ar.Members {
				m := &cur.ar.Members[i]
				h := headerBlock(cur, m)
				ob, ok := old[string(rune(m.Type))+"\x00"+m.Path]
				switch {
				case !ok:
					fresh++
					newHdrs = append(newHdrs, h...)
				case bytes.Equal(ob, h):
					same++
					model++ // a per-member "unchanged" flag
				default:
					changed := 0
					var which []string
					for _, fd := range fields {
						if !bytes.Equal(ob[fd.a:fd.b], h[fd.a:fd.b]) {
							changed += fd.b - fd.a
							which = append(which, fd.name)
						}
					}
					if len(which) == 1 && which[0] == "mtime" {
						mt++
					} else {
						other++
					}
					model += int64(2 + changed) // flag + field mask + changed field bytes (checksum is recomputed)
				}
			}
			model += int64(flateLen(newHdrs))
			s, _ := tarz3.Split(cur.plain, cur.ar)
			sNoTrail := s[:int64(len(s))-cur.ar.TrailerLen()]
			fl := int64(flateLen(sNoTrail))
			fmt.Printf("| %s | %s -> %s | %.1f | %s | %s | %s | %s | %s | %d | %d | %d / %d / %d / %d |\n", short(f), prev.label, cur.label, float64(P)/(1<<20),
				pct(cdcNew, P), pct(inS, P), pct(inU, P), pct(inC, P), pct(newPay, P), len(cur.ar.Members), newMem, same, mt, other, fresh)
			hrows = append(hrows, hrow{short(f), prev.label + " -> " + cur.label, P, int64(len(sNoTrail)), fl, model, newPay, same, mt, other, fresh})
			a := tot[short(f)]
			if a == nil {
				a = &agg{}
				tot[short(f)] = a
			}
			a.p += P
			a.cdcNew += cdcNew
			a.st += inS
			a.un += inU
			a.ch += inC
			a.memNew += newPay
			a.exact += int64(len(sNoTrail))
			a.flS += fl
			a.model += model
		}
	}
	fmt.Println("\nByte-weighted per family (all update pairs):")
	fmt.Println()
	fmt.Println("| family | plaintext CDC new % | in structure % | in unchanged-member payload % | in new-member payload % | member-level new payload % | exact headers % | DEFLATE(structure) % | per-path field-delta model % |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	var names []string
	for k := range tot {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		a := tot[k]
		fmt.Printf("| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", k, pct(a.cdcNew, a.p), pct(a.st, a.p), pct(a.un, a.p), pct(a.ch, a.p), pct(a.memNew, a.p), pct(a.exact, a.p), pct(a.flS, a.p), pct(a.model, a.p))
	}
	_ = hrows
}

func firstErr(es ...error) error {
	for _, e := range es {
		if e != nil {
			return e
		}
	}
	return nil
}
