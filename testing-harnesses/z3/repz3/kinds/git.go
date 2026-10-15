package kinds

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"zeros3-z3/gitz3"
	"zeros3-z3/lab"
	"zeros3-z3/repz3"
)

// Git is the Z3-16 gp-zlib tier (gitz3.SeqPlan, mode "zlib"): every pack entry's
// exact zlib stream is a component. Z3-21 moves everything that scales with the
// number of entries out of the representation object into one decoder-side table
//
//	uvarint(len meta) | meta | uvarint(n) | n x ( SHA-256 (32 B) | uvarint(length) )
//
// where meta is the gitz3 meta record (pack header, trailer, OFS distances, entry
// headers and delta bases: unchanged) and the entries are the streams in pack
// order. The table is cut with production CDC into a few STORED members and read
// whole by a pack reconstruction and by Expand; the streams are VIA. The
// representation object is therefore constant-size apart from one reference per
// table chunk and the index reference, and a point read never loads the table
// unless the object's stream was cut into several components. Tiny entries are not
// inlined (InlineMax 1): the contract has no inline piece, so every piece is a
// stream.
//
// The native index maps each object id (20 or 32 bytes, per the repository's
// object format) directly to the SHA-256 of the entry's first stream component:
// a point read is the representation object, O(log n) index probes and that one
// stream. The .idx file is not represented here (gitz3.IdxPlan derives it).

// GitStats describes a Git analysis.
type GitStats struct {
	Entries, Deltas, Streams int
	IndexRecords             int
	TableBytes               int
}

const maxGitTable = 256 << 20

// EncodeGit analyses a pack file.
func EncodeGit(pack []byte, algo gitz3.Algo) (*Bundle, *GitStats, error) {
	p, err := gitz3.ParsePack(pack, algo, gitz3.Limits{})
	if err != nil {
		return nil, nil, err
	}
	if _, err := p.Resolve(); err != nil {
		return nil, nil, err
	}
	an, err := gitz3.AnalyzePack(p)
	if err != nil {
		return nil, nil, err
	}
	pl, ps := an.SeqPlan(gitz3.RepOpts{Mode: "zlib", InlineMax: 1})
	meta, _ := pl.Detail["meta"].([]byte)
	out := &repz3.Rep{Kind: repz3.KindGit, Size: p.Size}
	var objs [][]byte
	table := binary.AppendUvarint(nil, uint64(len(meta)))
	table = append(table, meta...)
	table = binary.AppendUvarint(table, uint64(len(pl.Pieces)))
	streams := make([]repz3.SHA, len(pl.Pieces))
	for i := range pl.Pieces {
		c := pl.Pieces[i].Comp
		if c == nil {
			return nil, nil, fmt.Errorf("kinds: inline piece %d (the contract has none)", i)
		}
		b, err := c.Data()
		if err != nil {
			return nil, nil, err
		}
		streams[i] = repz3.SHA(c.ID)
		table = append(table, c.ID[:]...)
		table = binary.AppendUvarint(table, uint64(c.Size))
		objs = append(objs, b)
	}
	ms, tobjs, err := chunkTable(table)
	if err != nil {
		return nil, nil, err
	}
	out.Members, objs = ms, append(objs, tobjs...)
	pairs := map[string]repz3.SHA{}
	for i := range p.Entries {
		if fp := ps.FirstPiece[i]; fp < len(streams) {
			pairs[string(p.Entries[i].ID)] = streams[fp]
		}
	}
	recs, n, err := repz3.BuildIndex(algo.Size(), pairs)
	if err != nil {
		return nil, nil, err
	}
	objs = append(objs, recs)
	out.Indexes = []repz3.Index{{KeyWidth: algo.Size(), Count: n, Ref: sha(recs)}}
	b, err := newBundle(out, objs)
	return b, &GitStats{Entries: len(p.Entries), Deltas: ps.Deltas, Streams: len(pl.Pieces), IndexRecords: int(n), TableBytes: len(table)}, err
}

// chunkTable cuts a decoder table into STORED members with production CDC.
func chunkTable(table []byte) ([]repz3.Member, [][]byte, error) {
	var ms []repz3.Member
	var objs [][]byte
	err := lab.ChunkCDC(bytes.NewReader(table), func(_ int64, c []byte) error {
		cp := append([]byte(nil), c...)
		ms = append(ms, repz3.Member{Role: repz3.Stored, SHA: sha(cp), Size: int64(len(cp))})
		objs = append(objs, cp)
		return nil
	})
	return ms, objs, err
}

// readTable concatenates the first n members (all STORED, hashed on read) up to max bytes.
func readTable(c *repz3.Call, n int, max int64) ([]byte, error) {
	var buf []byte
	for i := 0; i < n; i++ {
		if c.Rep.Members[i].Role != repz3.Stored {
			return nil, fmt.Errorf("%w: table member %d is not a stored component", repz3.ErrCorrupt, i)
		}
		b, err := c.Member(i)
		if err != nil {
			return nil, err
		}
		if int64(len(buf))+int64(len(b)) > max {
			return nil, fmt.Errorf("%w: table over %d bytes", repz3.ErrLimit, max)
		}
		buf = append(buf, b...)
	}
	return buf, nil
}

type gitEntry struct {
	sha  repz3.SHA
	size int64
}

func gitTable(c *repz3.Call) (meta []byte, tab []gitEntry, err error) {
	if len(c.Rep.Recipe) != 0 || len(c.Rep.Members) == 0 {
		return nil, nil, fmt.Errorf("%w: Git representation shape", repz3.ErrCorrupt)
	}
	t, err := readTable(c, len(c.Rep.Members), maxGitTable)
	if err != nil {
		return nil, nil, err
	}
	ml, k := binary.Uvarint(t)
	if k <= 0 || ml > uint64(len(t)-k) {
		return nil, nil, fmt.Errorf("%w: Git table meta", repz3.ErrCorrupt)
	}
	meta, t = t[k:k+int(ml)], t[k+int(ml):]
	n, k := binary.Uvarint(t)
	if k <= 0 || n > uint64(len(t)-k)/33 {
		return nil, nil, fmt.Errorf("%w: Git stream table", repz3.ErrCorrupt)
	}
	out := make([]gitEntry, 0, n)
	pos := k
	for i := uint64(0); i < n; i++ {
		if len(t)-pos < 33 {
			return nil, nil, fmt.Errorf("%w: Git stream table entry %d", repz3.ErrCorrupt, i)
		}
		var e gitEntry
		copy(e.sha[:], t[pos:])
		pos += 32
		sz, k := binary.Uvarint(t[pos:])
		if k <= 0 || sz > 1<<40 {
			return nil, nil, fmt.Errorf("%w: Git stream length %d", repz3.ErrCorrupt, i)
		}
		e.size = int64(sz)
		pos += k
		out = append(out, e)
	}
	if pos != len(t) {
		return nil, nil, fmt.Errorf("%w: %d bytes after the Git stream table", repz3.ErrCorrupt, len(t)-pos)
	}
	return meta, out, nil
}

type gitDecoder struct{}

func (gitDecoder) Decode(c *repz3.Call) error {
	meta, tab, err := gitTable(c)
	if err != nil {
		return err
	}
	pieces := make([]lab.Piece, len(tab))
	size := map[lab.ID]int64{}
	for i, e := range tab {
		pieces[i] = lab.Piece{Comp: &lab.Comp{ID: lab.ID(e.sha), Size: e.size}}
		size[lab.ID(e.sha)] = e.size
	}
	fetched := 0
	err = gitz3.RebuildSeq(meta, pieces, func(id lab.ID) ([]byte, error) {
		fetched++
		return c.GetExact(repz3.SHA(id), size[id])
	}, c.W)
	if err == nil && fetched != len(pieces) {
		return fmt.Errorf("%w: Git recipe consumed %d of %d streams", repz3.ErrCorrupt, fetched, len(pieces))
	}
	return err
}

// Expand: the streams are named by the stored table.
func (gitDecoder) Expand(c *repz3.Call, emit func(repz3.SHA)) error {
	_, tab, err := gitTable(c)
	if err != nil {
		return err
	}
	for _, e := range tab {
		emit(e.sha)
	}
	return nil
}
