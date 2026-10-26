// Package dagz3 is the Z3-28 representation-DAG laboratory: bounded recursive discovery
// over the mature repz3 kinds, DAG/reachability/liveness measurement, and a global
// planner kept outside the durable wire contract.
//
// The split is deliberate and mirrors what a production integration needs:
//
//	analyzers (this file)  ->  exact, proven candidate DAGs (kinds.Bundle)
//	planner   (plan.go)    ->  one candidate per object, costed as a SHA-set union
//	lifecycle (graph.go)   ->  roots, mark/sweep, reachability, read-set accounting
//
// Nothing here is a ZeroS3 format. No planner state, root list or candidate identity is
// ever written into a repz3 object: a representation is a content-addressed object and
// the DAG is nothing but DERIVED.Child digests.
package dagz3

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/zstdz3"
)

// Lim is the limit set every depth-4 workload runs under: the hard maximum, opted in.
var Lim = repz3.Limits{MaxDepth: repz3.HardMaxDepth}

// Budget bounds one recursive discovery. Every limit is cumulative over the whole
// discovery (all levels, all siblings): a level is never granted its full allowance on
// its own. Hitting a limit is not an error: the affected payload falls back to the
// parent's ordinary representation (a stored member or CDC) and the stop is counted.
type Budget struct {
	MaxDepth    int   // deepest representation allowed (<= repz3.HardMaxDepth)
	Cap         int   // structural transforms allowed on any path; 0 = CDC only (the candidate "level")
	MaxAttempts int   // analyzer attempts (each parse + serialize + prove)
	MaxChildren int   // analyzer attempts per container
	MaxEntries  int   // tar entries a split may produce
	MaxInspect  int64 // bytes handed to analyzers
	MaxDecoded  int64 // bytes produced by decompression pre-flights
	MaxProved   int64 // bytes reconstructed by acceptance proofs
	MinMember   int   // members smaller than this are never analyzed
	CDCMin      int   // raw members at least this large get a CDC child (when depth allows)

	// MemberCap, when set, overrides Cap below a tar split: it returns how many further
	// structural levels member idx (0-based among the entries with a body) may use. A
	// planner uses it to enumerate heterogeneous candidates; discovery never calls it itself.
	MemberCap func(idx int) int

	// outputs
	Inspected, Decoded, Proved int64
	Attempts, Reps             int
	Accepted                   map[string]int
	Rejected                   map[string]int // magic matched, validity failed
	Stops                      map[string]int // a budget limit stopped the attempt

	memo   map[memoKey]*kinds.Bundle
	proofs map[repz3.SHA]bool
}

type memoKey struct {
	sum   [32]byte
	level int
	sl    int
	cap   int
	n     int
}

// DefaultBudget is the I0-shaped default: depth 4, generous but finite.
func DefaultBudget(capLevels int) *Budget {
	return &Budget{MaxDepth: repz3.HardMaxDepth, Cap: capLevels, MaxAttempts: 256, MaxChildren: 64, MaxEntries: 1 << 16,
		MaxInspect: 1 << 30, MaxDecoded: 256 << 20, MaxProved: 1 << 30, MinMember: 512, CDCMin: 16 << 10}
}

func (b *Budget) init() {
	if b.Accepted == nil {
		b.Accepted, b.Rejected, b.Stops = map[string]int{}, map[string]int{}, map[string]int{}
		b.memo, b.proofs = map[memoKey]*kinds.Bundle{}, map[repz3.SHA]bool{}
	}
	if b.MaxDepth <= 0 || b.MaxDepth > repz3.HardMaxDepth {
		b.MaxDepth = repz3.HardMaxDepth
	}
}

// Discover returns the exact representation of data under the budget: the best-structured
// proven candidate the detectors find within Cap levels, otherwise CDC. It is deterministic:
// the same bytes and budget produce the same representation objects.
func Discover(data []byte, b *Budget) (*kinds.Bundle, error) {
	b.init()
	out := b.analyze(data, 1, 0)
	if out == nil {
		return nil, errors.New("dagz3: no representation (CDC failed)")
	}
	// the top-level result is proven end to end whatever it is
	if err := b.prove(out, data); err != nil {
		return kinds.CDC(data)
	}
	return out, nil
}

// Depth is the nesting depth of a bundle tree (a bundle without derived children is 1).
func Depth(x *kinds.Bundle) int {
	d := 0
	for _, c := range x.Children {
		d = max(d, Depth(c))
	}
	return d + 1
}

func (b *Budget) stop(why string) { b.Stops[why]++ }

// analyze produces a proven representation of data to be stored at nesting depth level
// with sl structural transforms already applied above it. It never returns a bundle
// that has not been proven (a structural result) or that is not plain CDC.
func (b *Budget) analyze(data []byte, level, sl int) *kinds.Bundle {
	cdc := func() *kinds.Bundle {
		x, err := kinds.CDC(data)
		if err != nil {
			return nil
		}
		b.Reps++
		return x
	}
	if sl >= b.Cap || len(data) < b.MinMember {
		return cdc()
	}
	kind := detect(data)
	if kind == "" {
		return cdc()
	}
	needsChild := kind == "gzip" || kind == "zstd" || kind == "tar"
	if level+boolInt(needsChild) > b.MaxDepth {
		b.stop("depth")
		return cdc()
	}
	k := memoKey{sha256.Sum256(data), level, sl, b.Cap, len(data)}
	if m, ok := b.memo[k]; ok {
		if m == nil {
			return cdc()
		}
		return m
	}
	if b.Attempts >= b.MaxAttempts {
		b.stop("attempts")
		return cdc()
	}
	if b.Inspected+int64(len(data)) > b.MaxInspect {
		b.stop("inspect")
		return cdc()
	}
	b.Attempts++
	b.Inspected += int64(len(data))
	var res *kinds.Bundle
	var err error
	child := func(plain []byte) (*kinds.Bundle, error) {
		if c := b.analyze(plain, level+1, sl+1); c != nil {
			return c, nil
		}
		return nil, errors.New("dagz3: no child representation")
	}
	switch kind {
	case "sqlite":
		res, _, err = kinds.EncodeSQLite(bytes.NewReader(data), int64(len(data)))
	case "car":
		res, _, err = kinds.EncodeCAR(bytes.NewReader(data), int64(len(data)))
	case "gzip":
		if err = b.preflightGzip(data); err == nil {
			res, _, err = kinds.EncodeGzip(data, child)
		}
	case "zstd":
		if err = b.preflightZstd(data); err == nil {
			res, _, err = kinds.EncodeZstd(data, child)
		}
	case "tar":
		res, err = b.splitTar(data, level, sl)
	}
	if err == nil {
		if err = b.prove(res, data); err == nil {
			b.Accepted[kind]++
			b.Reps++
			b.memo[k] = res
			return res
		}
	}
	if errors.Is(err, errBudget) {
		b.stop(strings.TrimPrefix(err.Error(), errBudget.Error()+": "))
	} else {
		b.Rejected[kind]++
	}
	b.memo[k] = nil
	return cdc()
}

var errBudget = errors.New("budget")

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// prove is the acceptance gate: persist the bundle into an empty scratch store, rebuild it
// from the serialized objects only and compare length, SHA-256 and bytes (kinds.ProveLimits
// also checks that everything read is reachable). Results are cached by representation digest.
func (b *Budget) prove(x *kinds.Bundle, want []byte) error {
	if b.proofs[x.ID] {
		return nil
	}
	if b.Proved+int64(len(want)) > b.MaxProved {
		return fmt.Errorf("%w: proof", errBudget)
	}
	b.Proved += int64(len(want))
	if err := kinds.ProveLimits(nil, x, want, repz3.Limits{MaxDepth: b.MaxDepth}); err != nil {
		return err
	}
	b.proofs[x.ID] = true
	return nil
}

func (b *Budget) preflightGzip(gz []byte) error {
	left := b.MaxDecoded - b.Decoded
	if left <= 0 {
		return fmt.Errorf("%w: decoded", errBudget)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return err
	}
	n, err := io.Copy(io.Discard, io.LimitReader(zr, left+1))
	b.Decoded += n
	if n > left {
		return fmt.Errorf("%w: decoded", errBudget)
	}
	return err
}

func (b *Budget) preflightZstd(zst []byte) error {
	left := b.MaxDecoded - b.Decoded
	if left <= 0 {
		return fmt.Errorf("%w: decoded", errBudget)
	}
	var n countW
	_, err := zstdz3.Parse(bytes.NewReader(zst), &n, nil, zstdz3.Options{Limits: zstdz3.Limits{MaxTotalDecoded: left}})
	b.Decoded += int64(n)
	if errors.Is(err, zstdz3.ErrLimit) || int64(n) > left {
		return fmt.Errorf("%w: decoded", errBudget)
	}
	return err
}

type countW int64

func (c *countW) Write(p []byte) (int, error) { *c += countW(len(p)); return len(p), nil }

// detect only proposes a parser; the candidate is accepted after prove, never on the magic.
func detect(d []byte) string {
	switch {
	case len(d) >= 18 && d[0] == 0x1f && d[1] == 0x8b && d[2] == 8:
		return "gzip"
	case len(d) >= 13 && binary.LittleEndian.Uint32(d) == 0xFD2FB528:
		return "zstd"
	case len(d) >= 512 && bytes.HasPrefix(d, []byte("SQLite format 3\x00")):
		return "sqlite"
	case len(d) >= 1024 && bytes.HasPrefix(d[257:], []byte("ustar")):
		return "tar"
	case len(d) >= 64 && d[0] < 0x80 && (d[1] == 0xa2 || d[1] == 0xa1):
		return "car"
	}
	return ""
}

const tarBlock = 512

// splitTar is the generic container splitter on the contract: each 512-byte header is a
// STORED member; a body whose analysis yields a structural representation is a DERIVED
// member (child = that representation, frame = the zero padding); a large raw body is a
// DERIVED member over a CDC child; anything else is one STORED member. A member whose
// analysis fails stays an ordinary member and never affects its siblings.
func (b *Budget) splitTar(tar []byte, level, sl int) (*kinds.Bundle, error) {
	out := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(tar))}
	var objs [][]byte
	var kids []*kinds.Bundle
	stored := func(p []byte) {
		out.Members = append(out.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(p), Size: int64(len(p))})
		objs = append(objs, append([]byte(nil), p...))
	}
	tried, entries, bodies, pos := 0, 0, 0, 0
	for {
		if pos+tarBlock > len(tar) {
			return nil, fmt.Errorf("dagz3: tar ends inside a header at %d", pos)
		}
		hdr := tar[pos : pos+tarBlock]
		if allZero(hdr) {
			break
		}
		if entries++; entries > b.MaxEntries {
			return nil, fmt.Errorf("%w: entries", errBudget)
		}
		size, err := strconv.ParseInt(strings.Trim(string(hdr[124:136]), "\x00 "), 8, 64)
		if err != nil || size < 0 || hdr[124]&0x80 != 0 {
			return nil, fmt.Errorf("dagz3: tar entry at %d has no octal size", pos)
		}
		padded := (size + tarBlock - 1) / tarBlock * tarBlock
		if int64(len(tar)-pos-tarBlock) < padded {
			return nil, fmt.Errorf("dagz3: tar entry at %d is truncated", pos)
		}
		stored(hdr)
		body := tar[pos+tarBlock : pos+tarBlock+int(padded)]
		pos += tarBlock + int(padded)
		if padded == 0 {
			continue
		}
		data, pad := body[:size], body[size:]
		idx := bodies
		bodies++
		var child *kinds.Bundle
		memberCap := b.Cap
		if b.MemberCap != nil {
			memberCap = sl + 1 + b.MemberCap(idx)
		}
		if allZero(pad) && int(size) >= b.MinMember && memberCap > sl+1 && detect(data) != "" {
			if tried < b.MaxChildren {
				tried++
				saved := b.Cap
				b.Cap = memberCap
				if c := b.analyze(data, level+1, sl+1); c != nil && !isPlainCDC(c) {
					child = c
				}
				b.Cap = saved
			} else {
				b.stop("children")
			}
		}
		if child == nil && allZero(pad) && int(size) >= b.CDCMin && level+1 <= b.MaxDepth {
			child, err = kinds.CDC(data)
			if err != nil {
				return nil, err
			}
			b.Reps++
		}
		if child == nil {
			stored(body)
			continue
		}
		out.Members = append(out.Members, repz3.Member{Role: repz3.Derived, SHA: sha256.Sum256(body), Size: padded,
			Child: child.ID, Frame: repz3.Frame{Zeros: len(pad)}})
		kids = append(kids, child)
	}
	if pos < len(tar) {
		stored(tar[pos:])
	}
	return kinds.NewBundle(out, objs, kids...)
}

// IsPlainCDC reports whether x is the CDC fallback (a concat of stored members only).
func IsPlainCDC(x *kinds.Bundle) bool { return isPlainCDC(x) }

func isPlainCDC(x *kinds.Bundle) bool {
	if x.Rep.Kind != repz3.KindConcat {
		return false
	}
	for _, m := range x.Rep.Members {
		if m.Role != repz3.Stored {
			return false
		}
	}
	return true
}

func allZero(p []byte) bool {
	for _, c := range p {
		if c != 0 {
			return false
		}
	}
	return true
}
