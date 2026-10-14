package lab

import (
	"encoding/binary"
	"strconv"
)

// Z3-08H: recipe/reference layouts. A recipe lists, in order, the components
// (content-addressed blobs) and inline byte strings that rebuild an object.
// Each layout below is a concrete serialisation whose bytes are counted for
// every revision's real piece list; none is a proposed format.
//
//	json      the production manifest's chunkRef: {"sha256":"<64 hex>","length":N},
//	flat35    a flat 35 byte constant per reference (the Z3-07 accounting)
//	bin32     32-byte digest + varint(length<<1)    inline: varint(len<<1|1) + bytes
//	table     per-object digest table (32 B + varint length once per distinct blob),
//	          pieces are varint(index<<1) / inline as above
//	bin16     16-byte truncated digests (only if identity were store-assigned)
//	loc6      6-byte store-assigned locator + varint(length): the recipe does not
//	          carry the SHA-256 (the store verifies it when the blob is written)
//
// Per-recipe header bytes are identical across layouts and omitted.

// RecipeLayouts lists the layouts in report order.
var RecipeLayouts = []string{"json", "flat35", "bin32", "table", "bin16", "loc6"}

func uvarintLen(v uint64) int {
	var b [binary.MaxVarintLen64]byte
	return binary.PutUvarint(b[:], v)
}

// recipeBytes returns the serialised size of p's pieces under every layout.
func recipeBytes(p *Plan) map[string]int64 {
	out := map[string]int64{}
	idx := map[ID]int{}
	var tbl int64
	for _, pc := range p.Pieces {
		if pc.Comp == nil {
			n := int64(len(pc.Inline))
			inl := int64(uvarintLen(uint64(n)<<1|1)) + n
			out["json"] += n + InlineRecipeOverhead // the production format has no inline pieces; same charge as the flat model
			out["flat35"] += n + InlineRecipeOverhead
			out["bin32"] += inl
			out["table"] += inl
			out["bin16"] += inl
			out["loc6"] += inl
			continue
		}
		if pc.Via {
			continue
		}
		l := uint64(pc.Comp.Size)
		out["json"] += int64(len(`{"sha256":"`) + 64 + len(`","length":`) + len(strconv.FormatUint(l, 10)) + len(`},`))
		out["flat35"] += 35
		out["bin32"] += 32 + int64(uvarintLen(l<<1))
		out["bin16"] += 16 + int64(uvarintLen(l<<1))
		out["loc6"] += 6 + int64(uvarintLen(l<<1))
		i, ok := idx[pc.Comp.ID]
		if !ok {
			i = len(idx)
			idx[pc.Comp.ID] = i
			tbl += 32 + int64(uvarintLen(l))
		}
		out["table"] += int64(uvarintLen(uint64(i) << 1))
	}
	out["table"] += tbl + int64(uvarintLen(uint64(len(idx))))
	return out
}
