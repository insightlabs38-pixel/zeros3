package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"zeros3-z3/lab"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/sstz3"
)

// largest returns the biggest table of a revision.
func largest(fs []file) file {
	sort.Slice(fs, func(i, j int) bool { return len(fs[i].data) > len(fs[j].data) })
	return fs[0]
}

// cdcFetch is what ordinary CDC reads for [off, off+n): every chunk the range touches.
func cdcFetch(data []byte, off, n int64) (chunks int, bytes int64) {
	sizes, _ := lab.CDCSizes(strings.NewReader(string(data)))
	var pos int64
	for _, z := range sizes {
		if pos < off+n && pos+int64(z) > off {
			chunks++
			bytes += int64(z)
		}
		pos += int64(z)
	}
	return
}

func packedOf(st *repz3.MemStore, id repz3.SHA, read int64) int64 {
	b := st.Bytes(id)
	if int64(len(b)) == read {
		return lab.PackedLen(b)
	}
	return read
}

// cmdReads measures whole reads, small ranges and a point lookup of the representation
// against CDC for the largest table of the chain's first state, per producer.
func cmdReads(args []string) error {
	fs := flag.NewFlagSet("reads", flag.ExitOnError)
	root := fs.String("corpus", filepath.Join(os.Getenv("HOME"), ".cache", "z3-sst", "corpus"), "corpus root")
	target := fs.Int("target", 8<<10, "group target bytes")
	form := fs.Int("form", int(sstz3.FormUKV), "representation form")
	fs.Parse(args)
	fmt.Println("| producer | request | CDC fetch (chunks) | representation fetch | components | packed bytes | logical comp bytes | read ms |")
	fmt.Println("|---|---|---|---|---:|---:|---:|---:|")
	for _, prod := range []string{"leveldb", "rocksdb", "pebble"} {
		tables, _, err := loadRev(filepath.Join(*root, prod, "chain", "c0"))
		if err != nil {
			return err
		}
		f := largest(tables)
		b, _, _, err := kinds.EncodeSST(bytes.NewReader(f.data), int64(len(f.data)), sstz3.Options{Form: sstz3.Form(*form), Cutter: &sstz3.HashCutter{Target: *target, Max: 4 * *target}})
		if err != nil {
			return err
		}
		st := repz3.NewMemStore()
		id, _, _ := b.Persist(st)
		env := kinds.NewEnv(st)
		t, err := sstz3.OpenKeys(bytes.NewReader(f.data), int64(len(f.data)), sstz3.Limits{})
		if err != nil {
			return err
		}
		touched := func() (objs int, bytesRead, packed, logical int64) {
			_, tb := st.Touched()
			for obj, n := range tb {
				bytesRead += n
				if obj == id {
					continue
				}
				objs++
				packed += packedOf(st, obj, n)
				logical += n
			}
			return
		}
		row := func(name string, off, n int64, run func(c *repz3.Call) error) error {
			st.ResetReads()
			t0 := time.Now()
			c, err := env.Open(id)
			if err != nil {
				return err
			}
			if err := run(c); err != nil {
				return err
			}
			el := time.Since(t0)
			objs, br, packed, logical := touched()
			cdcN, cdcB := 0, int64(0)
			if n > 0 {
				cdcN, cdcB = cdcFetch(f.data, off, n)
			} else {
				cdcN, cdcB = cdcFetch(f.data, 0, int64(len(f.data)))
			}
			fmt.Printf("| %s | %s | %s (%d) | %s | %d | %s | %s | %.1f |\n", prod, name, human(cdcB), cdcN, human(br), objs, human(packed), human(logical), float64(el.Microseconds())/1000)
			return nil
		}
		if err := row(fmt.Sprintf("whole %s", human(int64(len(f.data)))), 0, 0, func(c *repz3.Call) error {
			var out bytes.Buffer
			if _, _, err := env.Reconstruct(id, &out); err != nil {
				return err
			}
			if !bytes.Equal(out.Bytes(), f.data) {
				return fmt.Errorf("whole read differs")
			}
			return nil
		}); err != nil {
			return err
		}
		mid := t.Blocks[len(t.Blocks)/2]
		inBlock := int64(mid.Off) + 100
		if err := row("4 KiB inside one block", inBlock, 4096, func(c *repz3.Call) error {
			got, _, err := kinds.ReadSSTRange(c, inBlock, 4096)
			if err == nil && !bytes.Equal(got, f.data[inBlock:inBlock+4096]) {
				err = fmt.Errorf("range differs")
			}
			return err
		}); err != nil {
			return err
		}
		span := int64(mid.Off+mid.Size+5) - 2048
		if err := row("4 KiB across two blocks", span, 4096, func(c *repz3.Call) error {
			got, _, err := kinds.ReadSSTRange(c, span, 4096)
			if err == nil && !bytes.Equal(got, f.data[span:span+4096]) {
				err = fmt.Errorf("range differs")
			}
			return err
		}); err != nil {
			return err
		}
		var key []byte
		sstz3.Records(bytes.NewReader(f.data), int64(len(f.data)), sstz3.Limits{}, func(uk []byte, _ uint64, _ []byte) error {
			if key == nil && len(uk) > 0 && bytes.Compare(uk, []byte("j/")) >= 0 {
				key = append([]byte(nil), uk...)
			}
			return nil
		})
		var ls kinds.LookupStats
		if err := row("point lookup (index + 1 block)", 0, 1, func(c *repz3.Call) error {
			blk, l, err := kinds.LookupSST(c, key)
			ls = l
			if err == nil && len(blk) < 5 {
				err = fmt.Errorf("empty block")
			}
			return err
		}); err != nil {
			return err
		}
		fmt.Printf("|  | lookup detail | index read through the tail: %d reads, %s | block %d of %d | %d groups | | | |\n", ls.TailReads, human(ls.TailBytes), ls.Block, len(t.Blocks), ls.Groups.Components)
		_ = sha256.Sum256
	}
	return nil
}

func human(n int64) string {
	switch {
	case n >= 10<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 10<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
