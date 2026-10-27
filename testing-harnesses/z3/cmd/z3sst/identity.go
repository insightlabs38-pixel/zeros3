package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

type idStats struct {
	n                                        int
	ikey, ukey, trailer, value, ukeyValue    int
	valueBytes, valueBytesSeen, ukeyValBytes int64
}

// cmdIdentity separates what is identical between two revisions at the record level: the full
// internal key (user key and sequence/type word), the user key, the sequence/type word, the
// value, and the (user key, value) pair. It answers how much reusable content the sequence
// numbers hide: records whose pair is unchanged but whose internal key is not.
func cmdIdentity(args []string) error {
	fs := flag.NewFlagSet("identity", flag.ExitOnError)
	root := fs.String("corpus", filepath.Join(os.Getenv("HOME"), ".cache", "z3-sst", "corpus"), "corpus root")
	fs.Parse(args)
	pairs := [][3]string{{"variants", "r0", "v03-update-compact"}, {"variants", "r0", "v04-insert-compact"}, {"variants", "r0", "v09-reexport"}, {"variants", "r0", "v10-churn"}, {"variants", "v09-reexport", "x01-reexport-half"}, {"variants", "r0", "n04-rekey"}}
	fmt.Println("| producer | A -> B | records in B | full internal key in A | user key in A | seq/type word in A | value in A | (user key, value) in A | value bytes in A |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---:|---:|")
	for _, prod := range []string{"leveldb", "rocksdb", "pebble"} {
		for _, p := range pairs {
			a, err := recordSet(filepath.Join(*root, prod, p[0], p[1]))
			if err != nil {
				return err
			}
			var st idStats
			var seenValue = map[[32]byte]bool{}
			for _, r := range a.recs {
				seenValue[r.v] = true
			}
			b, err := recordSet(filepath.Join(*root, prod, p[0], p[2]))
			if err != nil {
				return err
			}
			for _, r := range b.recs {
				st.n++
				if a.ikeys[r.ik] {
					st.ikey++
				}
				if a.ukeys[r.uk] {
					st.ukey++
				}
				if a.trailers[r.tr] {
					st.trailer++
				}
				if seenValue[r.v] {
					st.value++
					st.valueBytesSeen += int64(r.vl)
				}
				if a.pairs[[2]string{r.uk, string(r.v[:])}] {
					st.ukeyValue++
				}
				st.valueBytes += int64(r.vl)
			}
			pc := func(x int) string { return fmt.Sprintf("%.1f%%", 100*float64(x)/float64(st.n)) }
			fmt.Printf("| %s | %s -> %s | %d | %s | %s | %s | %s | %s | %.1f%% |\n", prod, p[1], p[2], st.n, pc(st.ikey), pc(st.ukey), pc(st.trailer), pc(st.value), pc(st.ukeyValue), 100*float64(st.valueBytesSeen)/float64(st.valueBytes))
		}
	}
	return nil
}

type recRow struct {
	ik, uk string
	tr     uint64
	v      [32]byte
	vl     int
}

type recSet struct {
	recs     []recRow
	ikeys    map[string]bool
	ukeys    map[string]bool
	trailers map[uint64]bool
	pairs    map[[2]string]bool
}

func recordSet(dir string) (*recSet, error) {
	tables, _, err := loadRev(dir)
	if err != nil {
		return nil, err
	}
	rs := &recSet{ikeys: map[string]bool{}, ukeys: map[string]bool{}, trailers: map[uint64]bool{}, pairs: map[[2]string]bool{}}
	for _, f := range tables {
		err := sstRecords(bytes.NewReader(f.data), int64(len(f.data)), func(uk []byte, tr uint64, v []byte) error {
			r := recRow{uk: string(uk), tr: tr, v: sha256.Sum256(v), vl: len(v)}
			r.ik = r.uk + string([]byte{byte(tr), byte(tr >> 8), byte(tr >> 16), byte(tr >> 24), byte(tr >> 32), byte(tr >> 40), byte(tr >> 48), byte(tr >> 56)})
			rs.recs = append(rs.recs, r)
			rs.ikeys[r.ik] = true
			rs.ukeys[r.uk] = true
			rs.trailers[tr] = true
			rs.pairs[[2]string{r.uk, string(r.v[:])}] = true
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return rs, nil
}
