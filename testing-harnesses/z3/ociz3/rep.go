package ociz3

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"

	"zeros3-z3/fstate"
	"zeros3-z3/lab"
)

// Representations over image states (sets of blob files). All of them leave
// manifests, configs, indexes and unknown blobs as exact bytes (production CDC
// chunks); they differ in what they do with layers.

func labPlan(name string, f fstate.File, format string) (*lab.Plan, error) {
	for _, r := range lab.Reps() {
		if r.Name == name {
			return r.Analyze(&lab.Input{Family: "oci", Format: format, Revision: f.Name, Src: lab.BytesSource(f.Name, f.Data), Opts: lab.DefaultOptions()})
		}
	}
	return nil, fmt.Errorf("no lab representation %q", name)
}

// Transform admission bounds. Analysis cost follows the inflated size, not the
// blob size, so a small hostile (or merely very sparse) gzip layer is bounded
// before any representation is built: it stays an ordinary CDC blob.
const (
	MaxTransformInflated = 256 << 20 // bytes of inflated tar
	MaxTransformRatio    = 128       // inflated / compressed
)

// Guard reports whether a layer file may be transformed: tar layers always (their
// size is the blob size), gzip layers when a bounded streaming inflate stays under
// both limits and the stream is well-formed.
func Guard(f fstate.File) bool {
	if Kind(f.Kind) != KindLayerGz {
		return true
	}
	limit := int64(MaxTransformInflated)
	if r := int64(len(f.Data)) * MaxTransformRatio; r < limit {
		limit = r
	}
	zr, err := gzip.NewReader(bytes.NewReader(f.Data))
	if err != nil {
		return false
	}
	n, err := io.Copy(io.Discard, io.LimitReader(zr, limit+1))
	return err == nil && n <= limit
}

func isLayer(f fstate.File) (format string, ok bool) {
	switch Kind(f.Kind) {
	case KindLayerGz:
		return "gzip", true
	case KindLayerTar:
		return "tar", true
	}
	return "", false
}

// LayerRep applies a lab representation to gzip and tar layers (gzName for
// gzip layers, tarName for uncompressed ones; "" = ordinary CDC) and CDC to
// everything else. A lab representation that does not apply, or fails, leaves
// the layer to CDC: a transform is never needed to accept a blob.
func LayerRep(name, gzName, tarName string, oracle bool) fstate.RepDef {
	return fstate.RepDef{Name: name, Oracle: oracle, Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) {
		format, ok := isLayer(f)
		if !ok {
			return fstate.FallbackCDC(f)
		}
		use := gzName
		if format == "tar" {
			use = tarName
		}
		if use == "" {
			return fstate.FallbackCDC(f)
		}
		if !Guard(f) {
			c.Counts["layer_guard"]++
			return fstate.FallbackCDC(f)
		}
		pl, err := labPlan(use, f, format)
		if err != nil {
			c.Counts["layer_fallback"]++
			return fstate.FallbackCDC(f)
		}
		c.Counts["layer_transformed"]++
		return pl, nil
	}}
}

// RegBlob models the registry's native granularity: every blob is one atomic,
// digest-addressed component; its sha256 digest is the component identity, so no
// mapping exists. It is a reference model, not a ZeroS3 representation (a ZeroS3
// component is at most one CDC chunk), and is marked Oracle.
func RegBlob() fstate.RepDef {
	return fstate.RepDef{Name: "reg-blob (model)", Oracle: true, Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) {
		id := lab.ID(sha256.Sum256(f.Data))
		data := f.Data
		comp := &lab.Comp{ID: id, Size: int64(len(data)), Data: func() ([]byte, error) { return data, nil }}
		return &lab.Plan{Pieces: []lab.Piece{{Comp: comp, Len: int64(len(data))}}, Oracle: true}, nil
	}}
}

// ArchiveRep plans the whole state as one exported OCI archive (deterministic
// tar of the layout): "cdc-archive" chunks the tar; "archive-tar" cuts it with the
// Z3-11 tar split (member payloads - the blobs - by content, CDC inside large ones).
func ArchiveRep(name, labName string) fstate.RepDef {
	return fstate.RepDef{Name: name, Object: func(st *fstate.State) []byte { return Archive(st.Files) }, Snapshot: func(c *fstate.Ctx, st *fstate.State) (*lab.Plan, error) {
		a := fstate.File{Name: "export.tar", Kind: "archive", Data: Archive(st.Files)}
		if labName == "cdc" {
			return fstate.Baseline("cdc", a)
		}
		return labPlan(labName, a, "tar")
	}}
}

// planCost estimates the incremental stored bytes of a plan against the current
// store: new components at their production-packed size, a reference per piece.
func planCost(c *fstate.Ctx, pl *lab.Plan) int64 {
	return planCostHas(c.Acct.Has, pl)
}

// intrinsicCost is planCost against an empty store: what the plan costs the first
// time its content is seen.
func intrinsicCost(pl *lab.Plan) int64 {
	return planCostHas(func(lab.ID) bool { return false }, pl)
}

func planCostHas(has func(lab.ID) bool, pl *lab.Plan) int64 {
	var n int64
	seen := map[lab.ID]bool{}
	for _, pc := range pl.Pieces {
		switch {
		case pc.Comp == nil:
			n += int64(len(pc.Inline)) + 1
		case pc.Via:
			if !has(pc.Comp.ID) && !seen[pc.Comp.ID] {
				seen[pc.Comp.ID] = true
				n += pc.Comp.Size
			}
		case has(pc.Comp.ID) || seen[pc.Comp.ID]:
			n += 34
		default:
			seen[pc.Comp.ID] = true
			sz := pc.Comp.Size
			if pc.Comp.Data != nil {
				if b, err := pc.Comp.Data(); err == nil {
					sz = lab.PackedLen(b)
				}
			}
			n += sz + 34
		}
	}
	return n + pl.MetaBytes + pl.IndexBytes
}

// Auto is the laboratory's per-layer chooser: for every layer blob it plans both
// ordinary CDC and the transformed representation and keeps the cheaper under the
// current store (so a layer whose plaintext is already stored is transformed, and a
// layer whose reconstruction metadata outweighs its saving is not). Unchanged
// digests never reach it (the runner skips a file whose name, which is its digest,
// is already stored); everything that is not a layer is CDC.
//
// With slack > 0 a layer is also transformed when, against an empty store, its
// transformed cost is within (1+slack) of CDC: the premium for being able to find
// the plaintext when the next revision arrives (a layer first stored as CDC leaves
// no plaintext to reuse, so the greedy rule alone never starts transforming).
func Auto(name, gzName, tarName string, slack float64) fstate.RepDef {
	return fstate.RepDef{Name: name, Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) {
		format, ok := isLayer(f)
		if !ok {
			return fstate.FallbackCDC(f)
		}
		cdc, err := fstate.FallbackCDC(f)
		if err != nil {
			return nil, err
		}
		if !Guard(f) {
			c.Counts["layer_guard"]++
			return cdc, nil
		}
		use := gzName
		if format == "tar" {
			use = tarName
		}
		tr, err := labPlan(use, f, format)
		if err != nil || tr.Rebuild == nil {
			c.Counts["layer_cdc"]++
			return cdc, nil
		}
		if planCost(c, tr) < planCost(c, cdc) || (slack > 0 && float64(intrinsicCost(tr)) <= (1+slack)*float64(intrinsicCost(cdc))) {
			c.Counts["layer_transformed"]++
			return tr, nil
		}
		c.Counts["layer_cdc"]++
		return cdc, nil
	}}
}

// Reps is the standard comparison set, in report order.
func Reps() []fstate.RepDef {
	reps := fstate.BaselineReps()
	reps = append(reps,
		ArchiveRep("cdc-archive", "cdc"),
		ArchiveRep("archive-tar", "tarmem-cdc64k"),
		RegBlob(),
		LayerRep("layer-oracle-logical", "oracle-logical", "", true),
		LayerRep("layer-oracle-tar", "oracle-tarmem-s", "", true),
		LayerRep("layer-gzip-exact", "irp-tok16k", "", false),
		LayerRep("layer-tar+gzip", "irp-tok16k+tarmem-s", "tarmem-s", false),
		LayerRep("layer-tar+gzip-cdc", "irp-tok16k+tarmem-cdc64k", "tarmem-cdc64k", false),
		Auto("oci-auto", "irp-tok16k+tarmem-cdc64k", "tarmem-cdc64k", 0),
		Auto("oci-auto-s10", "irp-tok16k+tarmem-cdc64k", "tarmem-cdc64k", 0.10),
	)
	return reps
}

// Runner returns the lab runner for OCI states: files are planned in the order
// given; the accounting parameters are the shared ones.
func Runner(refBytes int64, pack bool) *fstate.Runner {
	return &fstate.Runner{RefBytes: refBytes, Pack: pack}
}

var _ = bytes.Equal

// TransformPlan plans one layer blob under the transformed tar+gzip representation
// (exported for the read measurements).
func TransformPlan(f fstate.File, cdc bool) (*lab.Plan, error) {
	format, ok := isLayer(f)
	if !ok {
		return nil, fmt.Errorf("not a layer")
	}
	use := "irp-tok16k+tarmem-s"
	if cdc {
		use = "irp-tok16k+tarmem-cdc64k"
	}
	return labPlan(use, f, format)
}
