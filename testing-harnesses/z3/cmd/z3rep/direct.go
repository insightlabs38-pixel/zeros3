package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"strings"

	"zeros3-z3/carz3"
	"zeros3-z3/gitz3"
	"zeros3-z3/lab"
	"zeros3-z3/merklez3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/sqlitez3"
)

// directPath returns the pre-contract reconstruction of a stored stage: the format
// package's own decoder fed from the same stored bytes through a map getter, with
// no length checks, no hashing and no output bound. nil when no such path existed.
func directPath(ch *kinds.Chain, st *repz3.MemStore, rep *repz3.Rep, verify bool) func() {
	get := func(s [32]byte) ([]byte, error) {
		b := st.Bytes(s)
		if verify && sha256.Sum256(b) != s {
			return nil, errBad
		}
		return b, nil
	}
	var out io.Writer = io.Discard
	hash := func() { // the contract hashes its output; the verified variant does too
		if verify {
			out = sha256.New()
		}
	}
	switch {
	case ch.Name == "sqlite":
		rec := append([]byte(nil), rep.Recipe...)
		for _, m := range rep.Members {
			rec = append(rec, m.SHA[:]...)
		}
		return func() {
			sqlitez3.Decode(io.Discard, rec, func(i sqlitez3.ID) ([]byte, error) { return get(i) }, sqlitez3.Limits{})
		}
	case strings.HasPrefix(ch.Name, "car/"):
		hl, n := binary.Uvarint(rep.Recipe)
		head, tail := rep.Recipe[n:n+int(hl)], rep.Recipe[n+int(hl):]
		var table []byte
		for _, m := range rep.Members {
			table = append(table, st.Bytes(m.SHA)...)
		}
		return func() {
			hash()
			r, err := carz3.Unmarshal(bytes.Join([][]byte{head, table, tail}, nil))
			if err == nil {
				r.Reconstruct(out, get)
			}
		}
	case strings.HasPrefix(ch.Name, "git/"):
		pieces := make([]lab.Piece, len(rep.Members))
		for i, m := range rep.Members {
			pieces[i] = lab.Piece{Comp: &lab.Comp{ID: lab.ID(m.SHA), Size: m.Size}}
		}
		return func() {
			hash()
			gitz3.RebuildSeq(rep.Recipe, pieces, func(i lab.ID) ([]byte, error) { return get(i) }, out)
		}
	case strings.HasPrefix(ch.Name, "merkle/"):
		nodes := map[string]merklez3.Node{}
		var root merklez3.ID
		hl, n := binary.Uvarint(rep.Recipe[1:])
		root = merklez3.ID(rep.Recipe[1+n : 1+n+int(hl)])
		for _, o := range st.IDs() {
			if p := st.Bytes(o); len(p) > 1 && (p[0] == 1 || p[0] == 2) {
				if kids, err := merklez3.DecodeKids(p); err == nil {
					h := merklez3.HashSHA3_256.Sum(p)
					nodes[string(h)] = merklez3.Node{ID: merklez3.ID(h), Payload: p, Kids: kids}
				}
			}
		}
		return func() {
			hash()
			merklez3.Snapshot(mapStore{nodes, verify}, root, out)
		}
	}
	return nil
}

var errBad = errors.New("component does not match its digest")

type mapStore struct {
	m      map[string]merklez3.Node
	verify bool
}

func (s mapStore) Get(id merklez3.ID) (merklez3.Node, error) {
	n, ok := s.m[string(id)]
	if !ok {
		return merklez3.Node{}, merklez3.ErrMissing
	}
	if s.verify { // what the contract does per node: the component digest and the native hash
		if sha256.Sum256(n.Payload) == ([32]byte{}) || string(merklez3.HashSHA3_256.Sum(n.Payload)) != string(id) {
			return merklez3.Node{}, errBad
		}
	}
	return n, nil
}
