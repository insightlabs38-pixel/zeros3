package frontier

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"os"
	"sort"

	"zeros3-z3/lab"
)

// RefBytes is the manifest cost charged per production CDC chunk reference
// (the convention used by lab.Acct throughout Z3).
const RefBytes = 96

// NativeRefBytes is the cost of one native-block reference in a repz3-style
// recipe: a 32-byte digest plus a 4-byte length.
const NativeRefBytes = 36

// Cost is one artifact's cost under production CDC against everything the
// chain has stored so far.
type Cost struct {
	Raw       int64 `json:"raw"`
	Chunks    int   `json:"chunks"`
	NewChunks int   `json:"new_chunks"`
	NewBytes  int64 `json:"new_bytes"`  // new chunk bytes, loose
	Packed    int64 `json:"packed_new"` // same chunks under the production pack rule
	Incr      int64 `json:"incr"`       // Packed + RefBytes*Chunks (manifest)
	IncrLoose int64 `json:"incr_loose"` // NewBytes + RefBytes*Chunks
}

// Chain accounts a sequence of artifacts under production CDC + pack rule.
type Chain struct {
	seen map[[32]byte]struct{}
	Cum  Cost
	// OnNew, if set, is called for every chunk stored for the first time with its
	// stream offset, raw length and pack-rule size (used for cost attribution).
	OnNew func(off, n, packed int64)
}

func NewChain() *Chain { return &Chain{seen: map[[32]byte]struct{}{}} }

func (c *Chain) AddStream(r io.Reader) (Cost, error) {
	var k Cost
	err := lab.ChunkCDC(r, func(off int64, ch []byte) error {
		k.Raw += int64(len(ch))
		k.Chunks++
		id := sha256.Sum256(ch)
		if _, ok := c.seen[id]; !ok {
			c.seen[id] = struct{}{}
			k.NewChunks++
			k.NewBytes += int64(len(ch))
			p := lab.PackedLen(ch)
			k.Packed += p
			if c.OnNew != nil {
				c.OnNew(off, int64(len(ch)), p)
			}
		}
		return nil
	})
	k.Incr = k.Packed + RefBytes*int64(k.Chunks)
	k.IncrLoose = k.NewBytes + RefBytes*int64(k.Chunks)
	c.Cum.Raw += k.Raw
	c.Cum.Chunks += k.Chunks
	c.Cum.NewChunks += k.NewChunks
	c.Cum.NewBytes += k.NewBytes
	c.Cum.Packed += k.Packed
	c.Cum.Incr += k.Incr
	c.Cum.IncrLoose += k.IncrLoose
	return k, err
}

func (c *Chain) AddFile(path string) (Cost, error) {
	f, err := os.Open(path)
	if err != nil {
		return Cost{}, err
	}
	defer f.Close()
	return c.AddStream(f)
}

// BlockSet accounts native blocks (already compressed, so no pack rule) by
// content identity. Cost of a revision = new block bytes + one reference per block.
type BlockSet struct {
	seen map[[32]byte]struct{}
	Cum  int64
}

func NewBlockSet() *BlockSet { return &BlockSet{seen: map[[32]byte]struct{}{}} }

// BlockCost is the result of adding one revision's native blocks.
type BlockCost struct {
	Blocks, New     int
	Bytes, NewBytes int64
	Refs, Incr      int64
	ChangedFraction float64
}

func (b *BlockSet) Add(ids [][32]byte, sizes []int64) BlockCost {
	var c BlockCost
	c.Blocks = len(ids)
	for i, id := range ids {
		c.Bytes += sizes[i]
		if _, ok := b.seen[id]; !ok {
			b.seen[id] = struct{}{}
			c.New++
			c.NewBytes += sizes[i]
		}
	}
	c.Refs = NativeRefBytes * int64(len(ids))
	c.Incr = c.NewBytes + c.Refs
	if c.Bytes > 0 {
		c.ChangedFraction = float64(c.NewBytes) / float64(c.Bytes)
	}
	b.Cum += c.Incr
	return c
}

// Floor is the logical file-content oracle (labelled ORACLE in reports): every
// regular file's content is chunked by production CDC independently (hard-link
// peers once), plus a binary manifest of all entries (path, attributes, chunk
// digests) accounted through the same CDC chain. It approximates a store that
// separates metadata from content identity perfectly but still dedupes inside files.
type Floor struct {
	chunks   map[[32]byte]struct{}
	manifest *Chain
	objects  map[[32]byte]struct{} // whole-file objects (composefs-like control)
	Cum      FloorCost
}

type FloorCost struct {
	FileRaw, FilePacked int64 // new per-file CDC chunk bytes, loose / pack rule
	Manifest            int64 // CDC+pack incremental of the manifest blob
	ManifestRaw         int64
	Total               int64 // FilePacked + Manifest
	ObjNew              int   // new whole-file objects
	ObjBytes            int64 // their bytes (loose)
	Files               int
}

func NewFloor() *Floor {
	return &Floor{chunks: map[[32]byte]struct{}{}, manifest: NewChain(), objects: map[[32]byte]struct{}{}}
}

// Add records one revision from its materialised directory and tree.
func (f *Floor) Add(t *Tree, dir string) (FloorCost, error) {
	var k FloorCost
	var man []byte
	var tmp [binary.MaxVarintLen64]byte
	put := func(v uint64) { n := binary.PutUvarint(tmp[:], v); man = append(man, tmp[:n]...) }
	putS := func(s string) { put(uint64(len(s))); man = append(man, s...) }
	done := map[string]bool{}
	ix := t.index()
	for _, e := range t.Entries {
		putS(e.Path)
		put(uint64(e.Kind))
		put(uint64(e.Mode))
		put(uint64(e.Mtime))
		keys := make([]string, 0, len(e.Xattr))
		for kk := range e.Xattr {
			keys = append(keys, kk)
		}
		sort.Strings(keys)
		put(uint64(len(keys)))
		for _, kk := range keys {
			putS(kk)
			putS(e.Xattr[kk])
		}
		switch e.Kind {
		case KSymlink, KHard:
			putS(e.Link)
		case KFile:
			k.Files++
			sz, err := f.addFile(&k, dir+"/"+e.Path, &man, put, done)
			if err != nil {
				return k, err
			}
			put(uint64(sz))
		}
	}
	_ = ix
	mc, err := f.manifest.AddStream(bytesReader(man))
	if err != nil {
		return k, err
	}
	k.Manifest, k.ManifestRaw = mc.Incr, int64(len(man))
	k.Total = k.FilePacked + k.Manifest
	f.Cum.FileRaw += k.FileRaw
	f.Cum.FilePacked += k.FilePacked
	f.Cum.Manifest += k.Manifest
	f.Cum.ManifestRaw += k.ManifestRaw
	f.Cum.Total += k.Total
	f.Cum.ObjNew += k.ObjNew
	f.Cum.ObjBytes += k.ObjBytes
	f.Cum.Files += k.Files
	return k, nil
}

func (f *Floor) addFile(k *FloorCost, path string, man *[]byte, put func(uint64), done map[string]bool) (int64, error) {
	fh, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer fh.Close()
	whole := sha256.New()
	var ids [][32]byte
	var total int64
	err = lab.ChunkCDC(io.TeeReader(fh, whole), func(off int64, ch []byte) error {
		total += int64(len(ch))
		id := sha256.Sum256(ch)
		ids = append(ids, id)
		if _, ok := f.chunks[id]; !ok {
			f.chunks[id] = struct{}{}
			k.FileRaw += int64(len(ch))
			k.FilePacked += lab.PackedLen(ch)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	var wid [32]byte
	copy(wid[:], whole.Sum(nil))
	if _, ok := f.objects[wid]; !ok {
		f.objects[wid] = struct{}{}
		k.ObjNew++
		k.ObjBytes += total
	}
	put(uint64(len(ids)))
	for _, id := range ids {
		*man = append(*man, id[:]...)
	}
	return total, nil
}
