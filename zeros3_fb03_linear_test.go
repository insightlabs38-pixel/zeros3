package main

// FB-03 G-LINEAR: a small linearizability checker for per-key register
// histories (Wing and Gong's search with memoisation on the set of
// linearized operations and the register value). A history is a list of
// operations with invocation and response times taken from one monotonic
// clock; a write sets the key's value ("" is absent, a delete), a read
// observed a value. The checker is exact for histories up to a few hundred
// operations per key at the concurrency the tests use.

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

type linOp struct {
	call, ret int64 // nanoseconds on the recorder's clock; ret > call
	write     bool
	val       string // written or observed value; "" is absent
	label     string // for failure messages
}

// linRecorder collects per-key operations from concurrent goroutines.
type linRecorder struct {
	start time.Time
	mu    sync.Mutex
	ops   map[string][]linOp
}

func newLinRecorder() *linRecorder {
	return &linRecorder{start: time.Now(), ops: map[string][]linOp{}}
}

func (r *linRecorder) now() int64 { return int64(time.Since(r.start)) }

func (r *linRecorder) add(key string, op linOp) {
	r.mu.Lock()
	r.ops[key] = append(r.ops[key], op)
	r.mu.Unlock()
}

// check returns one error per key whose history is not linearizable from the
// given initial values (missing keys start absent).
func (r *linRecorder) check(init map[string]string) []error {
	var errs []error
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range r.ops {
			if !yield(k) {
				return
			}
		}
	}) {
		if ok, why := linCheck(r.ops[k], init[k]); !ok {
			errs = append(errs, fmt.Errorf("key %s: history of %d operations is not linearizable: %s", k, len(r.ops[k]), why))
		}
	}
	return errs
}

// linCheck reports whether ops (one key) has a linearization consistent
// with register semantics starting at init.
func linCheck(ops []linOp, init string) (bool, string) {
	ops = slices.Clone(ops)
	slices.SortFunc(ops, func(a, b linOp) int { return int(a.call - b.call) })
	n := len(ops)
	words := (n + 63) / 64
	done := make([]uint64, words)
	seen := map[string]bool{}
	key := func(state string) string {
		var b strings.Builder
		for _, w := range done {
			fmt.Fprintf(&b, "%016x", w)
		}
		b.WriteByte('|')
		b.WriteString(state)
		return b.String()
	}
	isDone := func(i int) bool { return done[i/64]&(1<<(i%64)) != 0 }
	set := func(i int, on bool) {
		if on {
			done[i/64] |= 1 << (i % 64)
		} else {
			done[i/64] &^= 1 << (i % 64)
		}
	}
	deepest, deepestAt := 0, ""
	var search func(state string, count int) bool
	search = func(state string, count int) bool {
		if count == n {
			return true
		}
		k := key(state)
		if seen[k] {
			return false
		}
		seen[k] = true
		// an operation may come next only if it was invoked before every
		// pending operation returned
		minRet := int64(1<<63 - 1)
		for i := range ops {
			if !isDone(i) && ops[i].ret < minRet {
				minRet = ops[i].ret
			}
		}
		for i := range ops {
			if isDone(i) || ops[i].call > minRet {
				continue
			}
			next := state
			if ops[i].write {
				next = ops[i].val
			} else if ops[i].val != state {
				if count >= deepest {
					deepest, deepestAt = count, fmt.Sprintf("%s read %q while the register held %q", ops[i].label, short(ops[i].val), short(state))
				}
				continue
			}
			set(i, true)
			if search(next, count+1) {
				return true
			}
			set(i, false)
		}
		return false
	}
	if search(init, 0) {
		return true, ""
	}
	return false, fmt.Sprintf("longest prefix %d; first conflict there: %s", deepest, deepestAt)
}

func short(v string) string {
	if len(v) > 12 {
		return v[:12]
	}
	return v
}
