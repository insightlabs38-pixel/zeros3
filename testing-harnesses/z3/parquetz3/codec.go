package parquetz3

import (
	"bytes"
	"fmt"
	"io"

	"zeros3-z3/brotliz3"
	"zeros3-z3/lz4z3"
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
	CompNone     = 0  // UNCOMPRESSED: payload == body
	CompSnapCmds = 1  // explicit Snappy commands
	CompSnapRef  = 2  // golang/snappy, fragment by fragment
	CompSnapCPP  = 3  // C++ snappy 1.1.x, 14-bit hash
	CompSnapCPP5 = 4  // C++ snappy with a 15-bit hash
	CompZstd     = 5  // zstdz3 skeleton + units
	CompSnapCRC  = 6  // C++ snappy 1.2.x built with CRC32C table hashing (DuckDB 2.0.0)
	CompLZ4Rec   = 7  // Z4-B6: lz4z3 recipe member (explicit sequences, hybrid, or a predictor with acceleration != 1)
	CompLZ4Def   = 8  // lz4z3.PredDefault, acceleration 1
	CompLZ4Cont  = 9  // lz4z3.PredContinue, acceleration 1
	CompLZ4Def5  = 10 // lz4z3.PredDefault5, acceleration 1
	CompBrotli5  = 11 // Z4-P7: brotliz3.Encode(body, quality 5..9, lgwin 22) = google/brotli one-shot (Arrow)
	CompBrotli9  = 15
	compMax      = 15
)

// CompName is the audit name of a compression id.
func CompName(id int) string {
	return [...]string{"none", "snappy-explicit", "snappy-go", "snappy-cpp14", "snappy-cpp15", "zstd-explicit", "snappy-cpp-crc15", "lz4-recipe", "lz4-default", "lz4-continue", "lz4-default5", "brotli-q5", "brotli-q6", "brotli-q7", "brotli-q8", "brotli-q9"}[id]
}

// CompRec describes how to rebuild a page's compressed payload from its body.
type CompRec struct {
	ID    uint8
	Cmds  []byte // CompSnapCmds: snappyz3.Marshal of the commands
	Zstd  []byte // CompZstd: uvarint(len skeleton) | skeleton | units
	LZ4   []byte // CompLZ4Rec: lz4z3.Recipe.Marshal
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
	return codec == CodecNone || codec == CodecSnappy || codec == CodecZstd || codec == CodecLZ4Raw || codec == CodecBrotli
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
	brLast    int // Brotli quality that reproduced the previous page (0 = none yet)
	brMiss    int // consecutive Brotli pages no ported quality reproduced
}

// brGiveUp: after this many consecutive unreproduced pages the producer is treated as
// unsupported and later pages are rejected without decoding or re-encoding.
const brGiveUp = 3

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
	case CodecLZ4Raw:
		body, seqs, err := lz4z3.Decode(payload, int(want), lz4z3.Limits{MaxDecoded: int(c.lim.MaxPage)})
		if err != nil {
			return nil, rec, corrupt("lz4_raw page: %v", err)
		}
		for _, p := range [...]struct {
			id   int
			comp uint8
		}{{lz4z3.PredDefault, CompLZ4Def}, {lz4z3.PredContinue, CompLZ4Cont}, {lz4z3.PredDefault5, CompLZ4Def5}} {
			if b, ok := lz4z3.PredictBlock(p.id, body, 1); ok && bytes.Equal(b, payload) {
				return body, CompRec{ID: p.comp}, nil // inline id: no recipe member
			}
		}
		rc := lz4z3.Analyze(body, seqs, true)
		if rep, e := rc.Replay(nil, body, lz4z3.Limits{MaxDecoded: int(c.lim.MaxPage)}); e != nil || !bytes.Equal(rep, payload) {
			return nil, rec, fmt.Errorf("%w: lz4 recipe differs from the page (%v)", ErrUnsupported, e)
		}
		return body, CompRec{ID: CompLZ4Rec, LZ4: rc.Marshal()}, nil
	case CodecBrotli:
		// Only streams with the 22-bit window header can come from the ported encoder path.
		if c.brMiss >= brGiveUp || len(payload) == 0 || payload[0]&0xF != 0xB {
			return nil, rec, unsupported("brotli page outside the reproduced encoder profile")
		}
		body, err := brotliz3.Decode(payload, int(want))
		if err != nil {
			if err == brotliz3.ErrCorrupt || err == brotliz3.ErrLimit {
				return nil, rec, corrupt("brotli page: %v", err)
			}
			return nil, rec, unsupported("brotli: %v", err)
		}
		if int64(len(body)) != want {
			return nil, rec, corrupt("brotli page decodes to %d bytes, header says %d", len(body), want)
		}
		order := [6]int{c.brLast, 8, 9, 5, 6, 7}
		for i, q := range order {
			if q == 0 || (i > 0 && q == c.brLast) {
				continue
			}
			if out, e := brotliz3.Encode(body, q, 22); e == nil && bytes.Equal(out, payload) {
				c.brLast, c.brMiss = q, 0
				return body, CompRec{ID: uint8(CompBrotli5 + q - 5)}, nil
			}
		}
		c.brMiss++
		return nil, rec, unsupported("brotli page not reproduced by qualities 5-9")
	}
	return nil, rec, unsupported("codec %s", CodecName(c.Codec))
}

func (c *Compressor) replayLZ4(rec CompRec, body []byte) ([]byte, error) {
	lim := lz4z3.Limits{MaxDecoded: int(c.lim.MaxPage)}
	var r lz4z3.Recipe
	switch rec.ID {
	case CompLZ4Def:
		r = lz4z3.Recipe{Kind: lz4z3.KindPredicted, Pred: lz4z3.PredDefault, Accel: 1}
	case CompLZ4Cont:
		r = lz4z3.Recipe{Kind: lz4z3.KindPredicted, Pred: lz4z3.PredContinue, Accel: 1}
	case CompLZ4Def5:
		r = lz4z3.Recipe{Kind: lz4z3.KindPredicted, Pred: lz4z3.PredDefault5, Accel: 1}
	default:
		var n int
		var err error
		if r, n, err = lz4z3.Unmarshal(rec.LZ4, lim); err != nil || n != len(rec.LZ4) {
			return nil, recipeErr("lz4 recipe: %v", err)
		}
	}
	return r.Replay(nil, body, lim)
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
	case CompLZ4Rec, CompLZ4Def, CompLZ4Cont, CompLZ4Def5:
		return c.replayLZ4(rec, body)
	}
	if rec.ID >= CompBrotli5 && rec.ID <= CompBrotli9 {
		out, err := brotliz3.Encode(body, int(rec.ID)-CompBrotli5+5, 22)
		if err != nil {
			return nil, recipeErr("brotli replay: %v", err)
		}
		return out, nil
	}
	return nil, recipeErr("compression id %d", rec.ID)
}
