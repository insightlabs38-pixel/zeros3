package lab

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// "preflate" representation: an external research oracle (preflate-rs, see
// oracle/preflate). Each raw DEFLATE stream is replaced by its plaintext
// (CDC-chunked into the family store) plus Preflate's reconstruction blob
// (charged as recipe metadata, never deduplicated). Wrapper bytes (gzip header
// and trailer) and non-DEFLATE regions are stored verbatim/CDC. A stream the
// oracle rejects falls back to CDC of its compressed bytes; that is an
// optimisation failure, reported in Detail. A missing binary makes the
// representation not applicable, so ordinary runs are unaffected.

// EnvPreflate names the z3preflate binary; otherwise the setup.sh cache path is tried.
const EnvPreflate = "Z3_PREFLATE"

// PreflateBinary returns the oracle binary path, if present.
func PreflateBinary() (string, bool) {
	cands := []string{os.Getenv(EnvPreflate)}
	cache := os.Getenv("Z3_CACHE")
	if cache == "" {
		if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
			cache = filepath.Join(x, "z3-oracle")
		} else if h, err := os.UserHomeDir(); err == nil {
			cache = filepath.Join(h, ".cache", "z3-oracle")
		}
	}
	if cache != "" {
		cands = append(cands, filepath.Join(cache, "preflate", "wrap", "target", "release", "z3preflate"))
	}
	for _, c := range cands {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, true
		}
	}
	return "", false
}

type oracleResult struct {
	OK       bool   `json:"ok"`
	Err      string `json:"err"`
	Consumed int64  `json:"consumed"`
	PlainLen int64  `json:"plain_len"`
	ReconLen int64  `json:"recon_len"`
	NS       int64  `json:"ns"`
	OutLen   int64  `json:"out_len"`
	Params   string `json:"params"`
}

func runOracle(bin string, args ...string) (oracleResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out, er bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &out, &er
	if err := cmd.Run(); err != nil {
		return oracleResult{}, fmt.Errorf("oracle: %v: %s", err, strings.TrimSpace(er.String()))
	}
	var r oracleResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		return r, fmt.Errorf("oracle: bad output %q", out.String())
	}
	return r, nil
}

// analyzeOracle runs the oracle on one raw DEFLATE stream.
func analyzeOracle(bin string, stream []byte) (plain, recon []byte, r oracleResult, err error) {
	dir, err := os.MkdirTemp("", "z3pf-")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	in, pl, rc := filepath.Join(dir, "in"), filepath.Join(dir, "plain"), filepath.Join(dir, "recon")
	if err = os.WriteFile(in, stream, 0o600); err != nil {
		return
	}
	if r, err = runOracle(bin, "analyze", in, pl, rc); err != nil || !r.OK {
		return
	}
	if plain, err = os.ReadFile(pl); err != nil {
		return
	}
	recon, err = os.ReadFile(rc)
	return
}

func recreateOracle(bin string, plain, recon []byte) ([]byte, oracleResult, error) {
	dir, err := os.MkdirTemp("", "z3pf-")
	if err != nil {
		return nil, oracleResult{}, err
	}
	defer os.RemoveAll(dir)
	pl, rc, out := filepath.Join(dir, "plain"), filepath.Join(dir, "recon"), filepath.Join(dir, "out")
	if err := os.WriteFile(pl, plain, 0o600); err != nil {
		return nil, oracleResult{}, err
	}
	if err := os.WriteFile(rc, recon, 0o600); err != nil {
		return nil, oracleResult{}, err
	}
	r, err := runOracle(bin, "recreate", pl, rc, out)
	if err != nil || !r.OK {
		return nil, r, err
	}
	b, err := os.ReadFile(out)
	return b, r, err
}

// FailClass reduces an oracle error message to a stable class.
func FailClass(msg string) string {
	switch {
	case msg == "":
		return ""
	case strings.Contains(msg, "NoCompressionCandidates") || strings.Contains(msg, "max_chain"):
		return "no-candidate-params"
	case strings.Contains(msg, "RoundtripMismatch"):
		return "roundtrip-mismatch"
	case strings.Contains(msg, "ReadDeflate") || strings.Contains(msg, "InvalidDeflate"):
		return "read-deflate"
	case strings.Contains(msg, "trailing"):
		return "trailing-data"
	}
	if i := strings.IndexByte(msg, ':'); i > 0 && i < 40 {
		return msg[:i]
	}
	return "other"
}

// gzipHeaderLen returns the length of the RFC1952 member header at the start of b.
func gzipHeaderLen(b []byte) (int, error) {
	if len(b) < 10 || b[0] != 0x1f || b[1] != 0x8b || b[2] != 8 {
		return 0, errors.New("not a gzip/deflate member")
	}
	flg, n := b[3], 10
	if flg&4 != 0 {
		if len(b) < n+2 {
			return 0, io.ErrUnexpectedEOF
		}
		n += 2 + int(binary.LittleEndian.Uint16(b[n:]))
	}
	for _, bit := range []byte{8, 16} {
		if flg&bit != 0 {
			i := bytes.IndexByte(b[min(n, len(b)):], 0)
			if i < 0 {
				return 0, io.ErrUnexpectedEOF
			}
			n += i + 1
		}
	}
	if flg&2 != 0 {
		n += 2
	}
	if n > len(b) {
		return 0, io.ErrUnexpectedEOF
	}
	return n, nil
}

type pfRegion struct {
	Off, Len int64
	Stream   bool // a raw DEFLATE stream
	Inline   bool // wrapper bytes kept verbatim in the recipe
	Stored   bool // the data of a stored (method 0) ZIP member (Z3-32 inner transforms)
}

// deflateRegions partitions the object into raw DEFLATE streams, wrapper bytes
// and other regions. ErrNotApplicable means the format carries none.
func deflateRegions(in *Input) ([]pfRegion, error) {
	size := in.Src.Size
	switch in.Format {
	case "deflate":
		return []pfRegion{{Off: 0, Len: size, Stream: true}}, nil
	case "gzip":
		head, err := in.Src.Span(0, min(size, 1<<20))
		if err != nil {
			return nil, err
		}
		h, err := gzipHeaderLen(head)
		if err != nil {
			return nil, err
		}
		if int64(h)+8 > size {
			return nil, io.ErrUnexpectedEOF
		}
		return []pfRegion{{Off: 0, Len: int64(h), Inline: true}, {Off: int64(h), Len: size - int64(h) - 8, Stream: true}, {Off: size - 8, Len: 8, Inline: true}}, nil
	case "zip", "pytorch-zip", "ndarray":
		segs, err := in.Segs()
		if err != nil {
			return nil, notApplicable(err)
		}
		var out []pfRegion
		n := 0
		for _, rg := range Regions(size, segs) {
			st := rg.Seg != nil && rg.Seg.Deflate
			if st {
				n++
			}
			out = append(out, pfRegion{Off: rg.Off, Len: rg.Len, Stream: st, Stored: rg.Seg != nil && !rg.Seg.Deflate})
		}
		if n == 0 {
			return nil, ErrNotApplicable
		}
		return out, nil
	}
	return nil, ErrNotApplicable
}

type pfStream struct {
	first, mid, last int // pieces [first,mid) plaintext chunks, [mid,last) recon chunks (recon-CDC mode)
	region           pfRegion
	recon            []byte
	plainLen         int64
}

func analyzePreflate(in *Input) (*Plan, error) { return analyzePreflateOpt(in, false) }

// analyzePreflateRCDC additionally CDC-chunks each reconstruction blob into the
// store, so any prefix shared with an earlier revision's blob is reused.
func analyzePreflateRCDC(in *Input) (*Plan, error) { return analyzePreflateOpt(in, true) }

func analyzePreflateOpt(in *Input, reconCDC bool) (*Plan, error) {
	bin, ok := PreflateBinary()
	if !ok {
		return nil, ErrNotApplicable
	}
	regions, err := deflateRegions(in)
	if err != nil {
		return nil, err
	}
	p := &Plan{}
	var streams []*pfStream
	var origStream, plainTotal, reconTotal, wrapper, fallback int64
	var nStream, nOK int
	var analyzeNS int64
	fails := map[string]int{}
	params := map[string]int{}
	for _, rg := range regions {
		switch {
		case rg.Inline:
			wrapper += rg.Len
			if err := appendRegion(p, in.Src, rg.Off, rg.Len, in.Opts); err != nil {
				return nil, err
			}
			continue
		case !rg.Stream:
			if rg.Len < in.Opts.InlineMax {
				err = appendRegion(p, in.Src, rg.Off, rg.Len, in.Opts)
			} else {
				err = appendChunked(p, in.Src, rg.Off, rg.Len, cdcSplit)
			}
			if err != nil {
				return nil, err
			}
			continue
		}
		nStream++
		origStream += rg.Len
		raw, err := in.Src.Span(rg.Off, rg.Len)
		if err != nil {
			return nil, err
		}
		plain, recon, res, err := analyzeOracle(bin, raw)
		if err != nil {
			return nil, err
		}
		analyzeNS += res.NS
		if !res.OK || res.Consumed != rg.Len || len(plain) == 0 {
			cls := FailClass(res.Err)
			if res.OK {
				cls = "trailing-data"
				if len(plain) == 0 {
					cls = "empty-plain"
				}
			}
			fails[cls]++
			fallback += rg.Len
			if err := appendChunked(p, in.Src, rg.Off, rg.Len, cdcSplit); err != nil {
				return nil, err
			}
			continue
		}
		nOK++
		params[res.Params]++
		st := &pfStream{first: len(p.Pieces), region: rg, recon: recon, plainLen: int64(len(plain))}
		if err := chunkBytes(p, plain); err != nil {
			return nil, err
		}
		st.mid = len(p.Pieces)
		if reconCDC {
			if err := chunkBytes(p, recon); err != nil {
				return nil, err
			}
		} else {
			p.MetaBytes += int64(len(recon))
		}
		st.last = len(p.Pieces)
		streams = append(streams, st)
		plainTotal += int64(len(plain))
		reconTotal += int64(len(recon))
	}
	detail := map[string]any{
		"streams": nStream, "streams_ok": nOK, "compressed_bytes": origStream, "plain_bytes": plainTotal,
		"recon_bytes": reconTotal, "wrapper_bytes": wrapper, "fallback_bytes": fallback,
		"analyze_ms": float64(analyzeNS) / 1e6,
	}
	if len(fails) > 0 {
		detail["fail_classes"] = fails
	}
	if len(params) > 0 {
		keys := make([]string, 0, len(params))
		for k := range params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 4 {
			keys = keys[:4]
		}
		detail["params"] = keys
	}
	detail["recon_in_store"] = reconCDC
	p.Detail = detail
	byFirst := map[int]*pfStream{}
	for _, s := range streams {
		byFirst[s.first] = s
	}
	var recreateNS int64
	p.Rebuild = func(w io.Writer, get func(ID) ([]byte, error)) error {
		for i := 0; i < len(p.Pieces); i++ {
			if s := byFirst[i]; s != nil {
				var plain []byte
				for _, pc := range p.Pieces[s.first:s.mid] {
					b, err := get(pc.Comp.ID)
					if err != nil {
						return err
					}
					plain = append(plain, b...)
				}
				recon := s.recon
				if reconCDC { // recon comes back from the store, not the closure
					recon = nil
					for _, pc := range p.Pieces[s.mid:s.last] {
						b, err := get(pc.Comp.ID)
						if err != nil {
							return err
						}
						recon = append(recon, b...)
					}
				}
				out, res, err := recreateOracle(bin, plain, recon)
				if err != nil {
					return err
				}
				if !res.OK {
					return fmt.Errorf("oracle recreate: %s", res.Err)
				}
				recreateNS += res.NS
				detail["recreate_ms"] = float64(recreateNS) / 1e6
				// independent exactness check against the original stream bytes
				orig, err := in.Src.Span(s.region.Off, s.region.Len)
				if err != nil {
					return err
				}
				if !bytes.Equal(out, orig) {
					return fmt.Errorf("oracle reconstruction differs from original stream (%d vs %d bytes)", len(out), len(orig))
				}
				if _, err := w.Write(out); err != nil {
					return err
				}
				i = s.last - 1
				continue
			}
			pc := p.Pieces[i]
			if pc.Comp == nil {
				if _, err := w.Write(pc.Inline); err != nil {
					return err
				}
				continue
			}
			b, err := get(pc.Comp.ID)
			if err != nil {
				return err
			}
			if _, err := w.Write(b); err != nil {
				return err
			}
		}
		return nil
	}
	return p, nil
}

// PreflateRecon runs the optional oracle on one raw DEFLATE stream and returns
// the reconstruction-blob size and the oracle's analyze time in ns. ok=false if
// the oracle is absent or rejects the stream.
func PreflateRecon(raw []byte) (recon int, analyzeNS int64, ok bool) {
	bin, have := PreflateBinary()
	if !have {
		return 0, 0, false
	}
	plain, rc, res, err := analyzeOracle(bin, raw)
	if err != nil || !res.OK || len(plain) == 0 {
		return 0, 0, false
	}
	return len(rc), res.NS, true
}
