package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"zeros3-z3/parquetz3"
)

type memStore map[parquetz3.ID][]byte

func (s memStore) emit(_ string, id parquetz3.ID, b []byte) error {
	if _, ok := s[id]; !ok {
		s[id] = append([]byte(nil), b...)
	}
	return nil
}

func (s memStore) get(id parquetz3.ID) ([]byte, error) {
	b, ok := s[id]
	if !ok {
		return nil, fmt.Errorf("component %x not stored", id[:6])
	}
	return b, nil
}

// cmdRT is the focused exact-reconstruction campaign: every corpus file is encoded, decoded
// from the serialized recipe and the stored components only, and compared by length, SHA-256
// and bytes. Files outside the format fall back (and must say why).
func cmdRT(args []string) error {
	fs := flag.NewFlagSet("rt", flag.ExitOnError)
	fams := fs.String("fam", "pyarrow-variants,duckdb-variants,pyarrow-chain,controls", "families")
	nop := fs.Bool("nopredict", false, "explicit recipes everywhere")
	fs.Parse(args)
	type group struct {
		name   string
		states []state
	}
	var groups []group
	for _, fam := range strings.Split(*fams, ",") {
		sts, _, err := loadFamily(fam)
		if err != nil {
			return err
		}
		groups = append(groups, group{fam, sts})
	}
	if rs := refStates(); len(rs) > 0 {
		groups = append(groups, group{"ref", rs})
	}
	fmt.Println("| group | files | exact | fallback to CDC | failures | MiB | encode MiB/s | decode MiB/s | logical chunk bytes | opaque chunk bytes |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	bad := 0
	for _, g := range groups {
		var files, exact, fb, fail int
		var bytesTot, logical, opaque int64
		var enc, dec time.Duration
		for _, s := range g.states {
			b, err := readFile(s.Path)
			if err != nil {
				return err
			}
			files++
			bytesTot += int64(len(b))
			st := memStore{}
			t0 := time.Now()
			rec, _, stats, err := parquetz3.Encode(bytes.NewReader(b), int64(len(b)), parquetz3.Options{NoPredict: *nop}, st.emit)
			enc += time.Since(t0)
			if err != nil {
				if parquetz3.Supported(err) {
					fail++
					bad++
					fmt.Fprintf(os.Stderr, "%s/%s: encode: %v\n", s.Fam, s.Rev, err)
				} else {
					fb++
				}
				continue
			}
			logical += stats.LogicalBytes
			opaque += stats.OpaqueBytes
			var out bytes.Buffer
			t0 = time.Now()
			err = parquetz3.Decode(&out, rec, st.get, parquetz3.Limits{})
			dec += time.Since(t0)
			if err != nil || out.Len() != len(b) || sha256.Sum256(out.Bytes()) != sha256.Sum256(b) || !bytes.Equal(out.Bytes(), b) {
				fail++
				bad++
				fmt.Fprintf(os.Stderr, "%s/%s: decode differs (%v)\n", s.Fam, s.Rev, err)
				continue
			}
			exact++
		}
		mbs := func(n int64, d time.Duration) string {
			if d <= 0 {
				return "-"
			}
			return fmt.Sprintf("%.0f", float64(n)/(1<<20)/d.Seconds())
		}
		fmt.Printf("| %s | %d | %d | %d | %d | %s | %s | %s | %s | %s |\n", g.name, files, exact, fb, fail, mib(bytesTot), mbs(bytesTot, enc), mbs(bytesTot, dec), mib(logical), mib(opaque))
	}
	if bad > 0 {
		return fmt.Errorf("%d files failed", bad)
	}
	return nil
}
