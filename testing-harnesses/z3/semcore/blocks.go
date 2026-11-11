package semcore

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

// Verified physical blocks.
//
// A range of a raw component must not be returned on the strength of a hash computed over
// that range alone: the component's identity is the flat SHA-256 of its whole payload. The
// mechanism here is first-touch verification followed by per-block checks:
//
//  1. the first time a process reads a range of a raw component it streams the whole blob
//     once (bounded buffer, no materialization), computing the flat SHA-256 and one SHA-256
//     per fixed-size block; the flat digest must equal the component's recorded payload
//     digest or the read fails closed (ErrCorrupt);
//  2. the resulting block table (32 bytes per block, held in process memory, never trusted
//     from disk) authenticates every later block read: a block is returned only if its
//     SHA-256 matches the verified table entry.
//
// Tampering with the blob after first touch is therefore caught per block; tampering before
// first touch is caught by the flat digest. A production integration replaces step 1 with the
// engine's existing authenticated chunk boundaries (chunk hashes are the block table).

// DefaultBlockSize is the verified block size used by range reads (0 disables verification
// and restores the unverified B3 behavior, kept only as the measured "before").
const DefaultBlockSize = 64 << 10

// BlockTable is the verified per-block digest list of one raw payload.
type BlockTable struct {
	BlockSize int
	Size      int64
	Sums      [][32]byte
}

// Bytes is the resident size of the table.
func (t *BlockTable) Bytes() int64 { return int64(len(t.Sums))*32 + 32 }

// buildBlockTable streams a blob once and verifies its flat digest.
func buildBlockTable(br BlobReader, want ID, size int64, bs int) (*BlockTable, error) {
	if br.Size() != size || size < 0 || size > MaxOutput || bs <= 0 {
		return nil, ErrCorrupt
	}
	t := &BlockTable{BlockSize: bs, Size: size}
	flat := sha256.New()
	buf := make([]byte, bs)
	for off := int64(0); off < size; off += int64(bs) {
		n := int64(bs)
		if size-off < n {
			n = size - off
		}
		if k, err := br.ReadAt(buf[:n], off); int64(k) < n {
			if err == nil || errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		flat.Write(buf[:n])
		t.Sums = append(t.Sums, sha256.Sum256(buf[:n]))
	}
	var got ID
	flat.Sum(got[:0])
	if got != want {
		return nil, fmt.Errorf("%w: blob digest mismatch on first touch", ErrCorrupt)
	}
	return t, nil
}

func (s *Store) blockTable(payload ID, size int64, bs int, io *IO) (*BlockTable, BlobReader, error) {
	br, err := s.B.OpenBlob(payload)
	if err != nil {
		return nil, nil, ErrMissing
	}
	s.mu.Lock()
	t := s.tables[payload]
	s.mu.Unlock()
	if t != nil && t.BlockSize == bs && t.Size == size {
		return t, br, nil
	}
	t, err = buildBlockTable(br, payload, size, bs)
	if err != nil {
		br.Close()
		return nil, nil, err
	}
	s.mu.Lock()
	if s.tables == nil {
		s.tables = map[ID]*BlockTable{}
	}
	s.tables[payload] = t
	s.Reads += size
	s.mu.Unlock()
	if io != nil {
		io.FirstTouchBytes += size
		io.FirstTouches++
	}
	return t, br, nil
}

type blockKey struct {
	payload ID
	idx     int64
}

// readVerified returns payload bytes [a,b) assembled from verified blocks. Blocks fetched
// during one request are kept in io (request-local cache) so a block is read and hashed once.
func (s *Store) readVerified(payload ID, size, a, b int64, io *IO) ([]byte, error) {
	bs := s.BlockSize
	t, br, err := s.blockTable(payload, size, bs, io)
	if err != nil {
		return nil, err
	}
	defer br.Close()
	if a < 0 || b > size || a > b {
		return nil, ErrCorrupt
	}
	out := make([]byte, b-a)
	for blk := a / int64(bs); blk*int64(bs) < b; blk++ {
		bk := blockKey{payload, blk}
		data, ok := io.blocks[bk]
		if !ok {
			lo := blk * int64(bs)
			n := int64(bs)
			if size-lo < n {
				n = size - lo
			}
			data = make([]byte, n)
			if k, err := br.ReadAt(data, lo); int64(k) < n {
				if err == nil {
					err = io2UnexpectedEOF
				}
				return nil, err
			}
			if sha256.Sum256(data) != t.Sums[blk] {
				return nil, fmt.Errorf("%w: block %d failed verification", ErrCorrupt, blk)
			}
			if io.blocks == nil {
				io.blocks = map[blockKey][]byte{}
			}
			io.blocks[bk] = data
			io.Bytes += n
			io.BlocksRead++
			io.page(payload, lo, n)
		}
		lo, hi := max(a, blk*int64(bs)), min(b, blk*int64(bs)+int64(len(data)))
		copy(out[lo-a:hi-a], data[lo-blk*int64(bs):hi-blk*int64(bs)])
	}
	io.Reads++
	return out, nil
}

var io2UnexpectedEOF = io.ErrUnexpectedEOF
