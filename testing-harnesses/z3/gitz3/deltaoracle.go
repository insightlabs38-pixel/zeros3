package gitz3

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// DeltaOracle runs Git's own create_delta() (oracle/gitdelta, built from Git's
// diff-delta.c at a pinned tag, fetched and compiled outside this repository).
// It is an optional research oracle: nothing in the parsers, representations or
// reconstruction uses it.
type DeltaOracle struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

// DeltaOraclePath returns the oracle binary or "" if it is not built.
func DeltaOraclePath() string {
	cands := []string{os.Getenv("Z3_GITDELTA")}
	if h, err := os.UserHomeDir(); err == nil {
		cands = append(cands, filepath.Join(h, ".cache", "z3-oracle", "gitdelta", "gitdelta"))
	}
	for _, c := range cands {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

func StartDeltaOracle() (*DeltaOracle, error) {
	bin := DeltaOraclePath()
	if bin == "" {
		return nil, fmt.Errorf("gitdelta oracle not built (oracle/gitdelta/setup.sh)")
	}
	return StartDeltaOracleAt(bin)
}

// StartDeltaOracleAt starts a specific oracle binary (e.g. another Git tag's build).
func StartDeltaOracleAt(bin string) (*DeltaOracle, error) {
	cmd := exec.Command(bin)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &DeltaOracle{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<20)}, nil
}

// Create returns Git's delta of target against base (ok=false if Git produced none).
func (o *DeltaOracle) Create(base, target []byte) (delta []byte, ok bool, err error) {
	var h [8]byte
	binary.LittleEndian.PutUint32(h[:], uint32(len(base)))
	binary.LittleEndian.PutUint32(h[4:], uint32(len(target)))
	w := bufio.NewWriter(o.in)
	w.Write(h[:])
	w.Write(base)
	w.Write(target)
	if err := w.Flush(); err != nil {
		return nil, false, err
	}
	var n [4]byte
	if _, err := io.ReadFull(o.out, n[:]); err != nil {
		return nil, false, err
	}
	l := binary.LittleEndian.Uint32(n[:])
	if l == 0xffffffff {
		return nil, false, nil
	}
	delta = make([]byte, l)
	if _, err := io.ReadFull(o.out, delta); err != nil {
		return nil, false, err
	}
	return delta, true, nil
}

func (o *DeltaOracle) Close() error {
	o.in.Close()
	return o.cmd.Wait()
}

// DeriveStats counts how many delta entries of a resolved pack are reproduced
// byte for byte by Git's create_delta(base, target).
type DeriveStats struct {
	Deltas          int
	Exact           int   // oracle delta == stored delta program, byte for byte
	Differ          int   // oracle produced a different program
	NoDelta         int   // oracle produced none
	ExactBytes      int64 // stored delta bytes that the oracle regenerates
	DifferBytes     int64
	TargetBytes     int64 // resolved target bytes of the delta entries
	DifferSizeDelta int64 // sum |len(oracle)-len(stored)| over differing programs
}

func DeriveDeltas(p *Pack, o *DeltaOracle) (DeriveStats, error) {
	var st DeriveStats
	cache := map[int][]byte{}
	for i := range p.Entries {
		e := &p.Entries[i]
		if !e.IsDelta() {
			continue
		}
		st.Deltas++
		stored, err := p.Inflate(i)
		if err != nil {
			return st, err
		}
		base, err := p.Object(e.Base, cache)
		if err != nil {
			return st, err
		}
		tgt, err := p.Object(i, cache)
		if err != nil {
			return st, err
		}
		st.TargetBytes += int64(len(tgt))
		d, ok, err := o.Create(base, tgt)
		if err != nil {
			return st, err
		}
		switch {
		case !ok:
			st.NoDelta++
			st.DifferBytes += int64(len(stored))
		case string(d) == string(stored):
			st.Exact++
			st.ExactBytes += int64(len(stored))
		default:
			st.Differ++
			st.DifferBytes += int64(len(stored))
			if len(d) > len(stored) {
				st.DifferSizeDelta += int64(len(d) - len(stored))
			} else {
				st.DifferSizeDelta += int64(len(stored) - len(d))
			}
		}
		if len(cache) > 256 {
			cache = map[int][]byte{}
		}
	}
	return st, nil
}

// Gain measures what delta derivation could remove between two packs: of the
// delta programs of B that A does not already contain, how many are exactly
// what Git's create_delta produces from objects A already holds. It is an
// oracle bound (the oracle is Git's C code), not a realisable representation.
type Gain struct {
	BDeltas         int
	ProgramsInA     int // B's delta program bytes also occur as a delta program in A (plain reuse)
	NewPrograms     int
	NewProgBytes    int64
	BaseOrTgtNotInA int
	Derivable       int
	DerivableBytes  int64
}

func DeriveGain(a, b *Pack, o *DeltaOracle) (Gain, error) {
	var g Gain
	progsA := map[string]bool{}
	for i := range a.Entries {
		if a.Entries[i].IsDelta() {
			pl, err := a.Inflate(i)
			if err != nil {
				return g, err
			}
			progsA[string(pl)] = true
		}
	}
	cacheA, cacheB := map[int][]byte{}, map[int][]byte{}
	for j := range b.Entries {
		e := &b.Entries[j]
		if !e.IsDelta() {
			continue
		}
		g.BDeltas++
		prog, err := b.Inflate(j)
		if err != nil {
			return g, err
		}
		if progsA[string(prog)] {
			g.ProgramsInA++
			continue
		}
		g.NewPrograms++
		g.NewProgBytes += int64(len(prog))
		bi := a.IndexOfID(b.Entries[e.Base].ID)
		ti := a.IndexOfID(e.ID)
		if bi < 0 || ti < 0 {
			g.BaseOrTgtNotInA++
			continue
		}
		bp, err := a.Object(bi, cacheA)
		if err != nil {
			return g, err
		}
		tp, err := a.Object(ti, cacheA)
		if err != nil {
			return g, err
		}
		d, ok, err := o.Create(bp, tp)
		if err != nil {
			return g, err
		}
		if ok && string(d) == string(prog) {
			g.Derivable++
			g.DerivableBytes += int64(len(prog))
		}
		if len(cacheA) > 256 {
			cacheA = map[int][]byte{}
		}
	}
	_ = cacheB
	return g, nil
}
