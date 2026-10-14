package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"zeros3-z3/carz3"
)

// cmdIdent compares native CID identity with the ZeroS3 SHA-256 identity of the
// block bytes across every archive of a corpus.
func cmdIdent(args []string) error {
	fs := flag.NewFlagSet("ident", flag.ExitOnError)
	scale := fs.String("scale", "tiny", "big|small|tiny")
	real := fs.Bool("real", true, "include the real samples")
	fs.Parse(args)
	sc, err := scaleByName(*scale)
	if err != nil {
		return err
	}
	type arch struct {
		name string
		data []byte
	}
	var all []arch
	for _, f := range carz3.Families(sc) {
		for _, r := range f.Revs {
			all = append(all, arch{f.Name + "/" + r.Label, r.Data})
		}
	}
	if *real {
		for _, n := range []string{"sample-v1.car", "sample-wrapped-v2.car"} {
			if b, err := os.ReadFile(filepath.Join(cacheDir(), n)); err == nil {
				all = append(all, arch{"real/" + n, b})
			}
		}
	}
	keyToSum := map[string][32]byte{}
	sumToKeys := map[[32]byte]map[string]bool{}
	var sections, ver, notver, derived, dupInFile, keyConflict int
	var cidBytes, blockBytes int64
	for _, a := range all {
		rd := bytes.NewReader(a.data)
		start := int64(0)
		if carz3.IsV2(a.data) {
			v, err := carz3.ParseV2(rd, int64(len(a.data)))
			if err != nil {
				continue
			}
			start = int64(v.DataOffset)
			a.data = a.data[start : start+int64(v.DataSize)]
		}
		r, err := carz3.NewReader(bytes.NewReader(a.data), carz3.DefaultLimits())
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for {
			e, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			sections++
			sum := sha256Of(e.Data)
			k := e.CID.Key()
			switch e.CID.Verify(e.Data) {
			case carz3.Verified:
				ver++
			case carz3.NotVerified:
				notver++
			}
			if e.CID.DerivableFrom256(sum) {
				derived++
			}
			if seen[string(e.CID.Raw)] {
				dupInFile++
			}
			seen[string(e.CID.Raw)] = true
			if old, ok := keyToSum[k]; ok && old != sum {
				keyConflict++
			}
			keyToSum[k] = sum
			if sumToKeys[sum] == nil {
				sumToKeys[sum] = map[string]bool{}
			}
			sumToKeys[sum][k] = true
			cidBytes += int64(len(e.CID.Raw))
			blockBytes += int64(len(e.Data))
		}
	}
	multi, extra := 0, 0
	for _, ks := range sumToKeys {
		if len(ks) > 1 {
			multi++
			extra += len(ks) - 1
		}
	}
	fmt.Printf("archives %d, sections %d, block bytes %d, CID bytes %d (%.2f%% of block bytes)\n", len(all), sections, blockBytes, cidBytes, 100*float64(cidBytes)/float64(blockBytes))
	fmt.Printf("CID multihash verified against payload: %d, not verifiable by the lab: %d\n", ver, notver)
	fmt.Printf("CID digest == ZeroS3 SHA-256 of the block (derivable): %d of %d\n", derived, sections)
	fmt.Printf("distinct native keys %d, distinct SHA-256 payloads %d\n", len(keyToSum), len(sumToKeys))
	fmt.Printf("native key -> two different payloads (would be corruption): %d\n", keyConflict)
	fmt.Printf("payloads known under >1 native identity: %d (extra identities %d)\n", multi, extra)
	fmt.Printf("sections repeating an exact CID already seen in the same archive: %d\n", dupInFile)
	return nil
}
