package wasmz3

import "zeros3-z3/idxref"

// Section types for the grouping cutters (idxref.Sec.Type): the module section id, 0x100 for the
// header.
const secHeader = 0x100

// RawSecs describes the module for the structure-only baselines and the cutters: one record
// section per module section (code: function bodies, data: segments, element: segments,
// everything else plain).
func RawSecs(m *Module, d []byte) []idxref.Sec {
	out := []idxref.Sec{{Type: secHeader, Off: 0, End: HeaderSize}}
	for _, s := range m.Dir.Secs {
		sc := idxref.Sec{Type: uint16(s.ID), Off: int(s.Off), End: int(s.End())}
		switch s.ID {
		case SecCode:
			for _, b := range m.Bodies {
				sc.Items = append(sc.Items, int(b.SizeOff))
			}
		case SecData:
			for _, g := range m.Datas {
				sc.Items = append(sc.Items, int(g.Off))
			}
		case SecElement:
			for _, g := range m.Elems {
				sc.Items = append(sc.Items, int(g.Off))
			}
		}
		out = append(out, sc)
	}
	return out
}
