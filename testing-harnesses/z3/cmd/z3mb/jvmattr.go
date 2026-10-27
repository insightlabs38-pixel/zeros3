package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"

	"zeros3-z3/jvmz3"
	"zeros3-z3/lab"
	"zeros3-z3/pdelta"
)

func init() { cmds["jvmattr"] = jvmAttr }

type jvmSet struct {
	names []string
	raw   [][]byte
	slots [][]jvmz3.Slot
	idx   map[string]int
}

func loadJvmSet(path string) (*jvmSet, error) {
	names, data, err := jarClasses(path)
	if err != nil {
		return nil, err
	}
	s := &jvmSet{names: names, raw: data, idx: map[string]int{}}
	for i, b := range data {
		c, _, err := jvmz3.Parse(bytes.NewReader(b), int64(len(b)), jvmz3.Limits{})
		if err != nil {
			s.slots = append(s.slots, nil)
		} else {
			s.slots = append(s.slots, c.Slots)
		}
		s.idx[names[i]] = i
	}
	return s, nil
}

func (s *jvmSet) masked(i int, sel func(*jvmz3.Slot) bool) []byte {
	b := append([]byte(nil), s.raw[i]...)
	if s.slots[i] != nil {
		jvmz3.Mask(b, s.slots[i], sel)
	}
	return b
}

func jvmAttr(args []string) error {
	old, err := loadJvmSet(args[0])
	if err != nil {
		return err
	}
	cur, err := loadJvmSet(args[1])
	if err != nil {
		return err
	}
	type v struct {
		name string
		sel  func(*jvmz3.Slot) bool
	}
	vars := []v{
		{"raw", func(*jvmz3.Slot) bool { return false }},
		{"cp-internal", func(s *jvmz3.Slot) bool { return s.Cls == jvmz3.ClCP }},
		{"code operands", func(s *jvmz3.Slot) bool { return s.Cls == jvmz3.ClCode }},
		{"attr/locals/...", func(s *jvmz3.Slot) bool {
			return s.Cls != jvmz3.ClCP && s.Cls != jvmz3.ClCode
		}},
		{"all slots", nil},
	}
	var total, ident, added int
	for i := range cur.raw {
		total += len(cur.raw[i])
		j, ok := old.idx[cur.names[i]]
		switch {
		case !ok:
			added += len(cur.raw[i])
		case sha256.Sum256(old.raw[j]) == sha256.Sum256(cur.raw[i]):
			ident += len(cur.raw[i])
		}
	}
	fmt.Printf("pair %s -> %s: %d -> %d classes; bytes %d: identical-by-name %d (%.1f%%), added %d\n", args[0], args[1], len(old.raw), len(cur.raw), total, ident, 100*float64(ident)/float64(total), added)
	fmt.Printf("%-16s %12s %14s %14s %12s\n", "variant", "new-class B", "changed lit B", "changed delta%", "same-masked")
	for _, vr := range vars {
		// whole-class identity: classes (anywhere in the old set) with identical (masked) bytes
		have := map[[32]byte]bool{}
		for i := range old.raw {
			have[sha256.Sum256(old.masked(i, vr.sel))] = true
		}
		var newB, chB, lit, cost, sameMasked int
		for i := range cur.raw {
			mb := cur.masked(i, vr.sel)
			if have[sha256.Sum256(mb)] {
				continue
			}
			newB += len(cur.raw[i])
			if j, ok := old.idx[cur.names[i]]; ok {
				chB += len(cur.raw[i])
				st := pdelta.Cost(old.masked(j, vr.sel), mb)
				lit += st.Literal
				cost += st.Cost
			}
		}
		_ = sameMasked
		fmt.Printf("%-16s %12d %14d %13.1f%% %12d\n", vr.name, newB, lit, 100*float64(cost)/float64(max(chB, 1)), chB)
	}
	_ = lab.PackedLen
	return nil
}
