package onnxz3

import (
	"fmt"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/scientificz3"
)

// Encode reuses Q12's checked region composition and existing Model PLANES.
// Raw/fine modes need only Concat/Stored; planes remains an analysis-only Model use.
func Encode(data []byte, p *Plan, mode string) (*kinds.Bundle, error) {
	if p == nil {
		return nil, ErrUnsupported
	}
	regions := &scientificz3.Plan{Format: "ONNX dense initializer"}
	for _, t := range p.Tensors {
		codec := "opaque"
		if mode != "raw" {
			codec = "raw"
		}
		regions.Regions = append(regions.Regions, scientificz3.Region{Off: t.Off, Len: t.Len, Codec: codec, Width: t.Width, Decoded: t.Len})
	}
	if mode != "raw" && mode != "fine" && mode != "planes" {
		return nil, fmt.Errorf("unknown ONNX grouping")
	}
	b, _, e := scientificz3.Encode(data, regions, mode == "planes")
	return b, e
}
