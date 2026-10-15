package ociz3

import (
	"encoding/hex"

	"zeros3-z3/fstate"
	"zeros3-z3/lab"
	"zeros3-z3/srev"
)

// RevisionOf expresses one blob's exact recipe in the shared shape. OCI needed
// nothing beyond the Z3-15F ingredients: a blob is a Component sequence (CDC chunks,
// or tar/DEFLATE components for a transformed layer), the registry digest is a
// native hint, and the per-layer reconstruction data is the Residual. The image
// graph itself is stored as ordinary component bytes (index/manifest/config JSON),
// so there are no Roots; layers are not derived from earlier revisions, so there
// are no Bases.
func RevisionOf(pl *lab.Plan, blobDigest string) *srev.Revision {
	if hexs, err := ParseDigest(blobDigest); err == nil {
		if raw, err := hex.DecodeString(hexs); err == nil {
			if pl.Detail == nil {
				pl.Detail = map[string]any{}
			}
			nat, _ := pl.Detail["natives"].(map[lab.ID][]byte)
			if nat == nil {
				nat = map[lab.ID][]byte{}
			}
			// the blob's own digest is native to the whole blob; it equals a component
			// identity only when the blob is a single component
			if len(pl.Pieces) == 1 && pl.Pieces[0].Comp != nil && hex.EncodeToString(pl.Pieces[0].Comp.ID[:]) == hexs {
				nat[pl.Pieces[0].Comp.ID] = raw
			}
			pl.Detail["natives"] = nat
		}
	}
	return lab.RevisionOfPlan(pl)
}

// NativeStat is the registry-digest bookkeeping of one image.
type NativeStat struct {
	Blobs         int   // verified blobs
	SingleChunk   int   // blob digest == one component's SHA-256: no mapping needed
	Multi         int   // blob digest names several components: needs a digest -> recipe entry
	MultiBytes    int64 // bytes of those blobs
	MappingBytes  int64 // recipe table bytes of the multi-chunk blobs (digest -> component list)
	SingleBytes   int64
	MultiChunks   int
	DigestEntries int64 // 32-byte blob digests that would be indexed
}

// Natives measures how the registry's identities map onto ZeroS3 components
// under production CDC.
func Natives(img *Image) (NativeStat, error) {
	var st NativeStat
	ac := lab.NewAcct(96, false)
	for _, n := range img.Nodes {
		if n.Data == nil || n.Status != "ok" {
			continue
		}
		st.Blobs++
		pl, err := fstate.FallbackCDC(fstate.File{Name: n.Digest, Data: n.Data})
		if err != nil {
			return st, err
		}
		rv := RevisionOf(pl, n.Digest)
		single := len(rv.Members) == 1 && len(rv.Members[0].C.Native) == 32
		if single {
			st.SingleChunk++
			st.SingleBytes += int64(len(n.Data))
		} else {
			st.Multi++
			st.MultiBytes += int64(len(n.Data))
			st.MultiChunks += len(rv.Members)
			st.DigestEntries++
		}
		acc, err := ac.Add(lab.PlanFromRevision(rv))
		if err != nil {
			return st, err
		}
		if !single {
			st.MappingBytes += acc.Layouts["table"]
		}
	}
	return st, nil
}
