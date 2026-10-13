package corpus

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"strconv"
)

// zipEntry is one member of a synthetic ZIP. The writer is deliberately tiny
// (no data descriptors, no zip64) so that every byte is under generator
// control; archive/zip can read its output.
type zipEntry struct {
	Name   string
	Data   []byte
	Method uint16 // 0 stored, 8 deflate
	DosT   uint16
	DosD   uint16
	Extra  []byte
	Align  int // pad Extra so the payload offset is a multiple of Align
}

func deflateBytes(b []byte) []byte {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, 6)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func writeZip(entries []zipEntry, comment string) []byte {
	var out, cd bytes.Buffer
	le := binary.LittleEndian
	for _, e := range entries {
		payload := e.Data
		if e.Method == 8 {
			payload = deflateBytes(e.Data)
		}
		extra := e.Extra
		if e.Align > 0 {
			base := out.Len() + 30 + len(e.Name) + len(extra)
			if pad := (e.Align - base%e.Align) % e.Align; pad > 0 {
				if pad < 4 {
					pad += e.Align
				}
				extra = append(append([]byte(nil), extra...), 0xFF, 0xFF, byte(pad-4), byte((pad-4)>>8))
				extra = append(extra, make([]byte, pad-4)...)
			}
		}
		crc := crc32.ChecksumIEEE(e.Data)
		off := out.Len()
		h := make([]byte, 30)
		le.PutUint32(h[0:], 0x04034b50)
		le.PutUint16(h[4:], 20)
		le.PutUint16(h[8:], e.Method)
		le.PutUint16(h[10:], e.DosT)
		le.PutUint16(h[12:], e.DosD)
		le.PutUint32(h[14:], crc)
		le.PutUint32(h[18:], uint32(len(payload)))
		le.PutUint32(h[22:], uint32(len(e.Data)))
		le.PutUint16(h[26:], uint16(len(e.Name)))
		le.PutUint16(h[28:], uint16(len(extra)))
		out.Write(h)
		out.WriteString(e.Name)
		out.Write(extra)
		out.Write(payload)

		c := make([]byte, 46)
		le.PutUint32(c[0:], 0x02014b50)
		le.PutUint16(c[4:], 0x031E)
		le.PutUint16(c[6:], 20)
		le.PutUint16(c[10:], e.Method)
		le.PutUint16(c[12:], e.DosT)
		le.PutUint16(c[14:], e.DosD)
		le.PutUint32(c[16:], crc)
		le.PutUint32(c[20:], uint32(len(payload)))
		le.PutUint32(c[24:], uint32(len(e.Data)))
		le.PutUint16(c[28:], uint16(len(e.Name)))
		le.PutUint16(c[30:], uint16(len(e.Extra))) // central extra excludes alignment padding
		le.PutUint32(c[38:], 0o644<<16)
		le.PutUint32(c[42:], uint32(off))
		cd.Write(c)
		cd.WriteString(e.Name)
		cd.Write(e.Extra)
	}
	cdOff := out.Len()
	out.Write(cd.Bytes())
	end := make([]byte, 22)
	le.PutUint32(end[0:], 0x06054b50)
	le.PutUint16(end[8:], uint16(len(entries)))
	le.PutUint16(end[10:], uint16(len(entries)))
	le.PutUint32(end[12:], uint32(cd.Len()))
	le.PutUint32(end[16:], uint32(cdOff))
	le.PutUint16(end[20:], uint16(len(comment)))
	out.Write(end)
	out.WriteString(comment)
	return out.Bytes()
}

// extTime builds an "extended timestamp" (0x5455) extra field.
func extTime(t uint32) []byte {
	b := []byte{0x55, 0x54, 5, 0, 1, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(b[5:], t)
	return b
}

func truthSets(parent, cur map[string]string) map[string][]string {
	t := map[string][]string{"unchanged": {}, "changed": {}, "added": {}, "removed": {}}
	for n, h := range cur {
		switch ph, ok := parent[n]; {
		case !ok:
			t["added"] = append(t["added"], n)
		case ph == h:
			t["unchanged"] = append(t["unchanged"], n)
		default:
			t["changed"] = append(t["changed"], n)
		}
	}
	for n := range parent {
		if _, ok := cur[n]; !ok {
			t["removed"] = append(t["removed"], n)
		}
	}
	for k := range t {
		sortStrings(t[k])
	}
	return t
}

func memberContent(r *RNG, i, n int) []byte {
	if i%3 == 0 {
		return r.Bytes(n)
	}
	return Text(r, n)
}

func contentKey(b []byte) string { return strconv.Itoa(len(b)) + ":" + hashHex(b) }

func init() {
	for _, method := range []uint16{0, 8} {
		mname := "stored"
		if method == 8 {
			mname = "deflate"
		}
		for _, mut := range []string{"change", "add", "remove", "reorder", "meta"} {
			method, mname, mut := method, mname, mut
			register(Family{Name: "zip-" + mname + "-" + mut, Format: "zip", Workload: "zip-members", Ext: ".zip",
				ToolDependent: method == 8,
				gen: func(c *Ctx, emit func(Artifact) error) error {
					r := c.RNG("data")
					count := 8 + c.Cfg.Revisions
					type mem struct {
						name string
						data []byte
						t    uint16
					}
					var ms []mem
					comment := "z3 corpus"
					extT := uint32(0)
					next := 0
					prevKeys := map[string]string{}
					return c.chain(emit, func(i int) ([]byte, string, map[string][]string) {
						desc := "base"
						if i == 0 {
							for ; next < count; next++ {
								ms = append(ms, mem{fmt.Sprintf("m%02d.dat", next), memberContent(r, next, c.size/count), 0x6000})
							}
							next = count
						} else {
							switch mut {
							case "change":
								k := r.Intn(len(ms))
								d := append([]byte(nil), ms[k].data...)
								copy(d[len(d)/2:], memberContent(r, k, len(d)/8))
								ms[k].data, ms[k].t = d, ms[k].t+uint16(i)
								desc = "rewrite 1/8 of " + ms[k].name
							case "add":
								ms = append(ms, mem{fmt.Sprintf("m%02d.dat", next), memberContent(r, next, c.size/count), 0x6000})
								next++
								desc = "append member " + ms[len(ms)-1].name
							case "remove":
								k := 1 + r.Intn(len(ms)-1)
								desc = "remove member " + ms[k].name
								ms = append(ms[:k:k], ms[k+1:]...)
							case "reorder":
								a := r.Intn(len(ms))
								b := (a + 1 + r.Intn(len(ms)-1)) % len(ms)
								ms[a], ms[b] = ms[b], ms[a]
								desc = "swap member positions"
							case "meta":
								extT = uint32(1_700_000_000 + 3600*i)
								comment = "z3 corpus rev " + strconv.Itoa(i)
								for k := range ms {
									ms[k].t += uint16(i)
								}
								desc = "bump DOS times, extended timestamps, archive comment"
							}
						}
						var es []zipEntry
						cur := map[string]string{}
						for _, m := range ms {
							e := zipEntry{Name: m.name, Data: m.data, Method: method, DosT: m.t, DosD: 0x5821}
							if extT != 0 {
								e.Extra = extTime(extT)
							}
							es = append(es, e)
							cur[m.name] = contentKey(m.data)
						}
						truth := truthSets(prevKeys, cur)
						if i == 0 {
							truth = nil
						}
						if mut == "reorder" && i > 0 {
							truth["reordered"] = []string{"true"}
						}
						prevKeys = cur
						return writeZip(es, comment), desc, truth
					})
				}})
		}
	}

	// PyTorch-style ZIP: stored, 64-byte-aligned storages, opaque data.pkl.
	for _, mut := range []string{"update-some", "add-storage", "pkl-only"} {
		mut := mut
		register(Family{Name: "torch-" + mut, Format: "pytorch-zip", Workload: "torch-zip", Ext: ".pt",
			gen: func(c *Ctx, emit func(Artifact) error) error {
				r := c.RNG("data")
				const n0 = 8
				var pkl []byte
				var stor [][]byte
				prevKeys := map[string]string{}
				return c.chain(emit, func(i int) ([]byte, string, map[string][]string) {
					desc := "base"
					if i == 0 {
						pkl = append([]byte{0x80, 0x02}, r.Bytes(2046)...)
						for k := 0; k < n0; k++ {
							stor = append(stor, r.Bytes(c.size/n0))
						}
					} else {
						switch mut {
						case "update-some":
							for k := 0; k < 2; k++ {
								j := r.Intn(len(stor))
								perturb(stor[j], 2, 0.02, r)
							}
							copy(pkl[100:], r.Bytes(16))
							desc = "perturb 2 storages, edit data.pkl"
						case "add-storage":
							stor = append(stor, r.Bytes(c.size/n0))
							pkl = append(pkl, r.Bytes(48)...)
							desc = "add storage, grow data.pkl"
						case "pkl-only":
							pkl = append(pkl, r.Bytes(5+r.Intn(60))...)
							desc = "grow data.pkl only"
						}
					}
					es := []zipEntry{
						{Name: "archive/data.pkl", Data: pkl, Align: 64},
						{Name: "archive/byteorder", Data: []byte("little"), Align: 64},
					}
					cur := map[string]string{"archive/data.pkl": contentKey(pkl), "archive/byteorder": contentKey([]byte("little")), "archive/version": contentKey([]byte("3\n"))}
					for k, s := range stor {
						name := fmt.Sprintf("archive/data/%d", k)
						es = append(es, zipEntry{Name: name, Data: s, Align: 64})
						cur[name] = contentKey(s)
					}
					es = append(es, zipEntry{Name: "archive/version", Data: []byte("3\n"), Align: 64})
					truth := truthSets(prevKeys, cur)
					if i == 0 {
						truth = nil
					}
					prevKeys = cur
					return writeZip(es, ""), desc, truth
				})
			}})
	}
}
