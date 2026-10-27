package parquetz3

import (
	"bytes"
	"fmt"
	"io"

	"zeros3-z3/snappyz3"
	"zeros3-z3/zstdz3"
)

// Compression replay. A page payload is a function of its decoded body only through the
// producer's encoder, which is not reproducible in general, so every page carries a
// CompRec: either a small predictor id (the reference encoder, run on the body, gives the
// payload byte for byte) or the explicit description of the stored stream (Snappy
// commands; the zstdz3 skeleton and units). A predictor id is recorded only after the
// predictor's output was compared with the original payload (the Z3-30 rule).
const (
	CompNone     = 0 // UNCOMPRESSED: payload == body
	CompSnapCmds = 1 // explicit Snappy commands
	CompSnapRef  = 2 // golang/snappy, fragment by fragment
	CompSnapCPP  = 3 // C++ snappy 1.1.x, 14-bit hash
	CompSnapCPP5 = 4 // C++ snappy with a 15-bit hash
	CompZstd     = 5 // zstdz3 skeleton + units
	CompSnapCRC  = 6 // C++ snappy 1.2.x built with CRC32C table hashing (DuckDB 2.0.0)
	compMax      = 6
)

// CompName is the audit name of a compression id.
func CompName(id int) string {
	return [...]string{"none", "snappy-explicit", "snappy-go", "snappy-cpp14", "snappy-cpp15", "zstd-explicit", "snappy-cpp-crc15"}[id]
}

// CompRec describes how to rebuild a page's compressed payload from its body.
type CompRec struct {
	ID    uint8
	Cmds  []byte // CompSnapCmds: snappyz3.Marshal of the commands
	Zstd  []byte // CompZstd: uvarint(len skeleton) | skeleton | units
	hints int
}

func snapEncoder(id uint8) func([]byte) []byte {
	switch id {
	case CompSnapRef:
		return snappyz3.EncodeRef
	case CompSnapCPP:
		return snappyz3.EncodeCPP
	case CompSnapCPP5:
		return snappyz3.EncodeCPP15
	case CompSnapCRC:
		return snappyz3.EncodeCPPCRC(15)
	}
	return nil
}

// Codecs reports whether the representation can replay a Parquet codec.
func Codecs(codec int) bool {
	return codec == CodecNone || codec == CodecSnappy || codec == CodecZstd
}

// CodecName names a Parquet codec for audits.
func CodecName(c int) string {
	n := [...]string{"UNCOMPRESSED", "SNAPPY", "GZIP", "LZO", "BROTLI", "LZ4", "ZSTD", "LZ4_RAW"}
	if c >= 0 && c < len(n) {
		return n[c]
	}
	return fmt.Sprint("CODEC", c)
}

// Compressor analyses and replays page payloads for one codec; it keeps the last
// predictor that worked as the first one to try (a producer uses one encoder).
type Compressor struct {
	Codec int
	// NoPredict skips the predictors: Snappy pages get explicit commands (measurement only).
	NoPredict bool
	lim       Limits
	last      uint8
}

// NewCompressor returns a compressor for a supported codec.
func NewCompressor(codec int, lim Limits) *Compressor {
	return &Compressor{Codec: codec, lim: lim.norm(), last: CompSnapCPP}
}

// Analyze decompresses payload (which must hold exactly want bytes) and describes how to
// rebuild it. The returned recipe is verified: Replay(rec, body) equals payload.
func (c *Compressor) Analyze(payload []byte, want int64) (body []byte, rec CompRec, err error) {
	switch c.Codec {
	case CodecNone:
		if int64(len(payload)) != want {
			return nil, rec, corrupt("uncompressed page of %d bytes declares %d", len(payload), want)
		}
		return payload, CompRec{ID: CompNone}, nil
	case CodecSnappy:
		body, cmds, err := snappyz3.Parse(payload, snappyz3.Limits{MaxDecoded: c.lim.MaxPage})
		if err != nil {
			return nil, rec, corrupt("snappy page: %v", err)
		}
		if int64(len(body)) != want {
			return nil, rec, corrupt("snappy page decodes to %d bytes, header says %d", len(body), want)
		}
		order := [4]uint8{CompSnapCPP, CompSnapCPP5, CompSnapCRC, CompSnapRef}
		if c.NoPredict {
			order = [4]uint8{}
		}
		for i, id := range order {
			if id == c.last {
				order[0], order[i] = order[i], order[0]
			}
		}
		for _, id := range order {
			if id != 0 && bytes.Equal(snappyz3.EncodeFragments(body, snapEncoder(id)), payload) {
				c.last = id
				return body, CompRec{ID: id}, nil
			}
		}
		return body, CompRec{ID: CompSnapCmds, Cmds: snappyz3.Marshal(cmds)}, nil
	case CodecZstd:
		var plain, units bytes.Buffer
		res, err := zstdz3.Parse(bytes.NewReader(payload), &plain, &units, zstdz3.Options{})
		if err != nil {
			return nil, rec, corrupt("zstd page: %v", err)
		}
		if int64(plain.Len()) != want {
			return nil, rec, corrupt("zstd page decodes to %d bytes, header says %d", plain.Len(), want)
		}
		z := appendUv(nil, uint64(len(res.Raw)))
		z = append(append(z, res.Raw...), units.Bytes()...)
		rec = CompRec{ID: CompZstd, Zstd: z}
		var out bytes.Buffer
		if err := c.replayZstd(&out, rec, plain.Bytes()); err != nil || !bytes.Equal(out.Bytes(), payload) {
			return nil, rec, fmt.Errorf("%w: zstd replay differs from the page (%v)", ErrUnsupported, err)
		}
		return plain.Bytes(), rec, nil
	}
	return nil, rec, unsupported("codec %s", CodecName(c.Codec))
}

func (c *Compressor) replayZstd(w io.Writer, rec CompRec, body []byte) error {
	n, k := readUv(rec.Zstd)
	if k <= 0 || n > uint64(len(rec.Zstd)-k) {
		return recipeErr("zstd recipe")
	}
	skel := rec.Zstd[k : k+int(n)]
	units := rec.Zstd[k+int(n):]
	_, err := zstdz3.Replay(w, skel, bytes.NewReader(units), bytes.NewReader(body), zstdz3.DefaultLimits())
	return err
}

// Replay rebuilds the compressed payload of body described by rec.
func (c *Compressor) Replay(rec CompRec, body []byte) ([]byte, error) {
	switch rec.ID {
	case CompNone:
		return body, nil
	case CompSnapRef, CompSnapCPP, CompSnapCPP5, CompSnapCRC:
		return snappyz3.EncodeFragments(body, snapEncoder(rec.ID)), nil
	case CompSnapCmds:
		cmds, err := snappyz3.Unmarshal(rec.Cmds, int64(len(body)), snappyz3.Limits{MaxDecoded: c.lim.MaxPage})
		if err != nil {
			return nil, recipeErr("snappy commands: %v", err)
		}
		var out bytes.Buffer
		if err := snappyz3.Replay(&out, body, cmds); err != nil {
			return nil, recipeErr("snappy replay: %v", err)
		}
		return out.Bytes(), nil
	case CompZstd:
		var out bytes.Buffer
		if err := c.replayZstd(&out, rec, body); err != nil {
			return nil, recipeErr("zstd replay: %v", err)
		}
		return out.Bytes(), nil
	}
	return nil, recipeErr("compression id %d", rec.ID)
}
