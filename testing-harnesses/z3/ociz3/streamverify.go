package ociz3

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"math"

	"zeros3-z3/zstdz3"
)

// LayerVerification is ephemeral evidence, not a representation kind or root.
type LayerVerification struct {
	Encoded, Plain int64
	Digest, DiffID string
	Opaque         bool
}
type layerCountReader struct {
	r io.Reader
	n int64
}

func (c *layerCountReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

type layerPlainHash struct {
	h      hash.Hash
	n, max int64
}

func (c *layerPlainHash) Write(p []byte) (int, error) {
	if int64(len(p)) > c.max-c.n {
		return 0, fmt.Errorf("%w: layer inflation", ErrLimit)
	}
	n, err := c.h.Write(p)
	c.n += int64(n)
	return n, err
}

// VerifyLayer streams native bytes and plaintext identities without retaining a
// blob. Unknown media types are verified as opaque bytes. No executable, registry
// client or image-graph closure is required by this production-shaped boundary.
func VerifyLayer(r io.Reader, d Descriptor, wantDiff string, limits Limits) (LayerVerification, error) {
	lim := limits.norm()
	var out LayerVerification
	if err := checkDesc(&d, lim); err != nil {
		return out, err
	}
	if d.Size == math.MaxInt64 {
		return out, fmt.Errorf("%w: layer size", ErrLimit)
	}
	want, err := ParseDigest(d.Digest)
	if err != nil {
		return out, err
	}
	encodedHash := sha256.New()
	count := &layerCountReader{r: io.TeeReader(io.LimitReader(r, d.Size+1), encodedHash)}
	plain := &layerPlainHash{h: sha256.New(), max: lim.MaxInflated}
	switch {
	case KindOf(d.MediaType) == KindLayerTar:
		_, err = io.Copy(plain, count)
	case KindOf(d.MediaType) == KindLayerGz:
		var zr *gzip.Reader
		zr, err = gzip.NewReader(count)
		if err == nil {
			_, err = io.Copy(plain, zr)
			closeErr := zr.Close()
			if err == nil {
				err = closeErr
			}
		}
	case d.MediaType == MTOCILayerZstd:
		_, err = zstdz3.Parse(count, plain, io.Discard, zstdz3.Options{Limits: zstdz3.Limits{MaxWindow: 16 << 20, MaxTotalDecoded: lim.MaxInflated, MaxFrameDecoded: lim.MaxInflated, MaxSkippable: 1 << 20, MaxIRBytes: 32 << 20, MaxFrames: 4096, MaxBlocks: 1 << 16}})
	default:
		out.Opaque = true
		_, err = io.Copy(io.Discard, count)
	}
	if err != nil {
		return out, err
	}
	out.Encoded = count.n
	out.Plain = plain.n
	out.Digest = "sha256:" + hex.EncodeToString(encodedHash.Sum(nil))
	if count.n != d.Size || hex.EncodeToString(encodedHash.Sum(nil)) != want {
		return out, ErrDigest
	}
	if out.Opaque {
		return out, nil
	}
	expected, err := ParseDigest(wantDiff)
	if err != nil {
		return out, err
	}
	out.DiffID = "sha256:" + hex.EncodeToString(plain.h.Sum(nil))
	if hex.EncodeToString(plain.h.Sum(nil)) != expected {
		return out, fmt.Errorf("%w: layer DiffID", ErrDigest)
	}
	return out, nil
}
