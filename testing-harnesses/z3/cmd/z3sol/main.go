// Command z3sol characterises Solana snapshot archives (Z3-15E). It reads an
// already-decompressed snapshot tar (zstd is not in the standard library:
// decompress with any zstd tool first) and reports container structure,
// AppendVec account layout, zero-fill, and the reuse available at tar-member,
// account-entry and raw-CDC level between two snapshots.
//
//	z3sol scan    SNAPSHOT.tar
//	z3sol compare A.tar B.tar
//	z3sol cdc     A.tar[.zst] B.tar[.zst]        raw CDC reuse of the files as given
//	z3sol estimate A.tar B.tar                   member+record pricing of B given A
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"zeros3-z3/lab"
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "z3sol:", err)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: z3sol scan|compare|cdc FILES...")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "scan":
		var s *snap
		s, err = scan(os.Args[2])
		if err == nil {
			s.print(os.Args[2])
		}
	case "compare":
		if len(os.Args) < 4 {
			fatal(fmt.Errorf("compare needs two tars"))
		}
		err = compare(os.Args[2], os.Args[3])
	case "estimate":
		if len(os.Args) < 4 {
			fatal(fmt.Errorf("estimate needs two tars"))
		}
		err = estimate(os.Args[2], os.Args[3])
	case "cdc":
		if len(os.Args) < 4 {
			fatal(fmt.Errorf("cdc needs two files"))
		}
		err = cdcCompare(os.Args[2], os.Args[3])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fatal(err)
	}
}

type member struct {
	name       string
	size       int64
	sha        [32]byte
	zeros      int64
	entries    int // AppendVec accounts
	entryBytes int64
	parseOK    bool
}

type snap struct {
	size    int64
	members []member
	// account entries by truncated SHA-256 of the exact record bytes
	entries  map[uint64]int64 // key -> bytes
	owners   map[[32]byte]*ownerStat
	dataHist map[int]int         // log2 bucket of data_len -> entries
	dups     int                 // entries whose exact bytes repeat inside the snapshot
	byKey    map[[40]byte]uint64 // (pubkey, write_version) -> truncated SHA-256 of the record
	keyDup   int
	zeroHash int // records whose stored 32-byte account hash is all zero
	execs    int
	totEnt   int
	totEntB  int64
	maxEnt   int64
}

type ownerStat struct {
	n int
	b int64
}

const avHeader = 48 + 56 + 32 // StoredMeta + AccountMeta + account hash

func trunc(b []byte) uint64 { s := sha256.Sum256(b); return binary.LittleEndian.Uint64(s[:8]) }

// parseAppendVec walks the accounts of one AppendVec file image.
func parseAppendVec(b []byte, fn func(rec []byte, owner [32]byte, dataLen uint64)) (n int, used int64, ok bool) {
	p := 0
	for p+avHeader <= len(b) {
		dl := binary.LittleEndian.Uint64(b[p+8:])
		if dl > 10<<20 {
			return n, used, false
		}
		exe := b[p+48+16+32]
		if exe > 1 {
			return n, used, false
		}
		end := p + avHeader + int(dl)
		end = (end + 7) &^ 7
		if end > len(b) {
			return n, used, false
		}
		var owner [32]byte
		copy(owner[:], b[p+48+16:])
		if fn != nil {
			fn(b[p:end], owner, dl)
		}
		n++
		used += int64(end - p)
		p = end
		// the remainder of a file is zero padding once the writer stopped
		if p < len(b) && allZero(b[p:min(len(b), p+avHeader)]) {
			break
		}
	}
	return n, used, true
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func zeroCount(b []byte) int64 {
	var n int64
	for _, c := range b {
		if c == 0 {
			n++
		}
	}
	return n
}

func scan(path string) (*snap, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, _ := f.Stat()
	s := &snap{size: st.Size(), entries: map[uint64]int64{}, byKey: map[[40]byte]uint64{}, owners: map[[32]byte]*ownerStat{}, dataHist: map[int]int{}}
	tr := tar.NewReader(bufio.NewReaderSize(f, 1<<20))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		m := member{name: h.Name, size: h.Size}
		hs := sha256.New()
		isAV := strings.HasPrefix(h.Name, "accounts/")
		var buf []byte
		if isAV {
			buf = make([]byte, 0, h.Size)
		}
		chunk := make([]byte, 1<<20)
		for {
			n, rerr := tr.Read(chunk)
			if n > 0 {
				hs.Write(chunk[:n])
				m.zeros += zeroCount(chunk[:n])
				if isAV {
					buf = append(buf, chunk[:n]...)
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				return nil, rerr
			}
		}
		copy(m.sha[:], hs.Sum(nil))
		if isAV {
			seen := map[uint64]bool{}
			m.entries, m.entryBytes, m.parseOK = parseAppendVec(buf, func(rec []byte, owner [32]byte, dl uint64) {
				if allZero(rec[48+56 : avHeader]) {
					s.zeroHash++
				}
				if rec[48+16+32] == 1 {
					s.execs++
				}
				k := trunc(rec)
				var nk [40]byte
				copy(nk[:32], rec[16:48])
				copy(nk[32:], rec[0:8])
				if _, d := s.byKey[nk]; d {
					s.keyDup++
				}
				s.byKey[nk] = k
				if _, dup := s.entries[k]; dup {
					s.dups++
				}
				s.entries[k] = int64(len(rec))
				_ = seen
				o := s.owners[owner]
				if o == nil {
					o = &ownerStat{}
					s.owners[owner] = o
				}
				o.n++
				o.b += int64(len(rec))
				b := 0
				for x := dl; x > 0; x >>= 1 {
					b++
				}
				s.dataHist[b]++
				s.totEnt++
				s.totEntB += int64(len(rec))
				s.maxEnt = max(s.maxEnt, int64(len(rec)))
			})
		}
		s.members = append(s.members, m)
	}
	return s, nil
}

func (s *snap) print(path string) {
	var accN, accB, accZ, accEnt int
	var accEntB int64
	var big []member
	var okN, badN int
	var other []member
	for _, m := range s.members {
		if strings.HasPrefix(m.name, "accounts/") {
			accN++
			accB += int(m.size)
			accZ += int(m.zeros)
			accEnt += m.entries
			accEntB += m.entryBytes
			if m.parseOK {
				okN++
			} else {
				badN++
			}
			big = append(big, m)
		} else {
			other = append(other, m)
		}
	}
	sort.Slice(big, func(i, j int) bool { return big[i].size > big[j].size })
	fmt.Printf("%s: %d bytes on disk, %d members\n\n", path, s.size, len(s.members))
	fmt.Printf("| member group | members | bytes | zero bytes |\n|---|--:|--:|--:|\n")
	for _, m := range other {
		fmt.Printf("| %s | 1 | %d | %.1f%% |\n", m.name, m.size, 100*float64(m.zeros)/float64(max(1, m.size)))
	}
	fmt.Printf("| accounts/* (AppendVec files) | %d | %d | %.1f%% |\n", accN, accB, 100*float64(accZ)/float64(max(1, accB)))
	fmt.Printf("\nAppendVec: %d files parse cleanly, %d do not; %d account records in %d bytes (%.2f%% of AppendVec bytes; the rest is zero fill / unparsed), largest record %d bytes, records repeating exact bytes already seen in this snapshot: %d; records with an all-zero stored account hash: %d; executable accounts: %d\n", okN, badN, accEnt, accEntB, 100*float64(accEntB)/float64(max(1, int64(accB))), s.maxEnt, s.dups, s.zeroHash, s.execs)
	fmt.Printf("\nlargest AppendVec files:\n\n| file | bytes | zero bytes | records | parsed bytes |\n|---|--:|--:|--:|--:|\n")
	for _, m := range big[:min(8, len(big))] {
		fmt.Printf("| %s | %d | %.1f%% | %d | %d |\n", m.name, m.size, 100*float64(m.zeros)/float64(max(1, m.size)), m.entries, m.entryBytes)
	}
	type ob struct {
		o [32]byte
		s *ownerStat
	}
	var obs []ob
	for o, st := range s.owners {
		obs = append(obs, ob{o, st})
	}
	sort.Slice(obs, func(i, j int) bool { return obs[i].s.b > obs[j].s.b })
	fmt.Printf("\ntop owners by record bytes (owner program id, hex prefix):\n\n| owner | records | bytes | share |\n|---|--:|--:|--:|\n")
	for _, o := range obs[:min(6, len(obs))] {
		fmt.Printf("| %x | %d | %d | %.1f%% |\n", o.o[:6], o.s.n, o.s.b, 100*float64(o.s.b)/float64(max(1, s.totEntB)))
	}
	var keys []int
	for k := range s.dataHist {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	fmt.Printf("\nrecord data_len histogram (bits): ")
	for _, k := range keys {
		fmt.Printf("%d:%d ", k, s.dataHist[k])
	}
	fmt.Println()
}

func compare(pa, pb string) error {
	a, err := scan(pa)
	if err != nil {
		return err
	}
	b, err := scan(pb)
	if err != nil {
		return err
	}
	am := map[string]member{}
	for _, m := range a.members {
		am[m.name] = m
	}
	var same, diff, onlyB int
	var sameB, diffB, onlyBB, totB int64
	for _, m := range b.members {
		totB += m.size
		o, ok := am[m.name]
		switch {
		case !ok:
			onlyB++
			onlyBB += m.size
		case o.sha == m.sha:
			same++
			sameB += m.size
		default:
			diff++
			diffB += m.size
		}
	}
	onlyA := 0
	bm := map[string]bool{}
	for _, m := range b.members {
		bm[m.name] = true
	}
	for _, m := range a.members {
		if !bm[m.name] {
			onlyA++
		}
	}
	fmt.Printf("member level (B against A by name): %d members, %d bytes in B\n\n| class | members | bytes | share |\n|---|--:|--:|--:|\n| identical name and bytes | %d | %d | %.1f%% |\n| same name, different bytes | %d | %d | %.1f%% |\n| only in B | %d | %d | %.1f%% |\n\nmembers only in A: %d\n",
		len(b.members), totB, same, sameB, 100*float64(sameB)/float64(totB), diff, diffB, 100*float64(diffB)/float64(totB), onlyB, onlyBB, 100*float64(onlyBB)/float64(totB), onlyA)
	// account entries
	var found, foundB, miss, missB int64
	for k, n := range b.entries {
		if _, ok := a.entries[k]; ok {
			found++
			foundB += n
		} else {
			miss++
			missB += n
		}
	}
	// native identity (pubkey, write_version) as an equality hint
	var kHit, kAgree, kMissing int
	for k, h := range b.byKey {
		if ha, ok := a.byKey[k]; ok {
			kHit++
			if ha == h {
				kAgree++
			}
		} else {
			kMissing++
		}
	}
	fmt.Printf("\nnative identity (pubkey, write_version): %d keys in B; %d present in A, of which %d are byte-identical records (%d disagree); %d not in A; keys repeated inside one snapshot: A %d, B %d\n", len(b.byKey), kHit, kAgree, kHit-kAgree, kMissing, a.keyDup, b.keyDup)
	fmt.Printf("\naccount-record level (distinct records by SHA-256 of the exact bytes): B has %d distinct records (%d bytes); %d (%d bytes, %.1f%%) already in A; %d (%d bytes) new\n",
		len(b.entries), foundB+missB, found, foundB, 100*float64(foundB)/float64(max(1, foundB+missB)), miss, missB)
	return nil
}

func cdcSets(path string) (map[[32]byte]int, int64, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	set := map[[32]byte]int{}
	var total int64
	n := 0
	err = lab.ChunkCDC(bufio.NewReaderSize(f, 1<<20), func(_ int64, c []byte) error {
		set[sha256.Sum256(c)] = len(c)
		total += int64(len(c))
		n++
		return nil
	})
	return set, total, n, err
}

func cdcCompare(pa, pb string) error {
	as, atot, an, err := cdcSets(pa)
	if err != nil {
		return err
	}
	bf, err := os.Open(pb)
	if err != nil {
		return err
	}
	defer bf.Close()
	var btot, reuse, reuseDistinct, newB, newPacked int64
	var bn int
	seen := map[[32]byte]bool{}
	err = lab.ChunkCDC(bufio.NewReaderSize(bf, 1<<20), func(_ int64, c []byte) error {
		id := sha256.Sum256(c)
		btot += int64(len(c))
		bn++
		_, inA := as[id]
		switch {
		case inA:
			reuse += int64(len(c))
		case seen[id]:
			reuse += int64(len(c))
		default:
			newB += int64(len(c))
			newPacked += lab.PackedLen(c)
			seen[id] = true
		}
		_ = reuseDistinct
		return nil
	})
	if err != nil {
		return err
	}
	var aDistinct int64
	for _, l := range as {
		aDistinct += int64(l)
	}
	fmt.Printf("A %s: %d bytes, %d chunks, %d distinct chunks (%d bytes distinct)\nB %s: %d bytes, %d chunks; reused (in A or earlier in B): %d bytes (%.1f%%), new: %d bytes (%.1f%%), pack-compressed %d bytes\n",
		pa, atot, an, len(as), aDistinct, pb, btot, bn, reuse, 100*float64(reuse)/float64(btot), newB, 100*float64(newB)/float64(btot), newPacked)
	_ = bytes.Equal
	return nil
}

// estimate prices B given A under three storage shapes (new bytes, then the
// production pack-compression estimate of what is new):
//
//	tar CDC          production CDC over the decompressed tar
//	member+record    identical members referenced whole; other AppendVec files
//	                 split into account records (new ones stored, zero fill free);
//	                 bank fields / status cache cut with CDC
func estimate(pa, pb string) error {
	a, err := scan(pa)
	if err != nil {
		return err
	}
	// chunk sets of A's non-account members (bank fields, status cache)
	aChunks := map[[32]byte]bool{}
	am := map[string][32]byte{}
	for _, m := range a.members {
		am[m.name] = m.sha
	}
	fa, _ := os.Open(pa)
	tr := tar.NewReader(bufio.NewReaderSize(fa, 1<<20))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeDir || strings.HasPrefix(h.Name, "accounts/") {
			continue
		}
		lab.ChunkCDC(tr, func(_ int64, c []byte) error { aChunks[sha256.Sum256(c)] = true; return nil })
	}
	fa.Close()

	fb, err := os.Open(pb)
	if err != nil {
		return err
	}
	defer fb.Close()
	tb := tar.NewReader(bufio.NewReaderSize(fb, 1<<20))
	var refs, recNew, recNewPacked, recReused, otherNew, otherNewPacked, zeroFill, hdrBytes, memberBytes int64
	var reusedMembers, newRecs int
	for {
		h, err := tb.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		memberBytes += h.Size
		hdrBytes += 512
		data, err := io.ReadAll(tb)
		if err != nil {
			return err
		}
		sh := sha256.Sum256(data)
		if old, ok := am[h.Name]; ok && old == sh {
			reusedMembers++
			refs++
			continue
		}
		if strings.HasPrefix(h.Name, "accounts/") {
			var fresh []byte
			parseAppendVec(data, func(rec []byte, _ [32]byte, _ uint64) {
				if _, ok := a.entries[trunc(rec)]; ok {
					recReused += int64(len(rec))
					refs++
					return
				}
				newRecs++
				recNew += int64(len(rec))
				fresh = append(fresh, rec...)
			})
			recNewPacked += lab.PackedLen(fresh)
			_, used, _ := parseAppendVec(data, nil)
			zeroFill += int64(len(data)) - used
			continue
		}
		// bank fields / status cache: production CDC against A's chunks
		var fresh []byte
		lab.ChunkCDC(bytes.NewReader(data), func(_ int64, c []byte) error {
			if aChunks[sha256.Sum256(c)] {
				refs++
				return nil
			}
			otherNew += int64(len(c))
			fresh = append(fresh, c...)
			return nil
		})
		otherNewPacked += lab.PackedLen(fresh)
	}
	recipe := refs*35 + hdrBytes
	fmt.Printf("B = %s (%d member bytes)\n\nmember+record representation, given A:\n\n| part | bytes |\n|---|--:|\n| members referenced whole (identical name+bytes) | %d members |\n| account records reused from A | %d bytes |\n| NEW account records | %d bytes in %d records, pack-compressed %d |\n| AppendVec zero fill not stored (run length only) | %d bytes |\n| NEW bank-fields / status-cache chunks (CDC vs A) | %d bytes, pack-compressed %d |\n| references (%d x 35 B) + tar headers (512 B x members) | %d |\n| **total, pack-compressed** | **%d** |\n",
		pb, memberBytes, reusedMembers, recReused, recNew, newRecs, recNewPacked, zeroFill, otherNew, otherNewPacked, refs, recipe, recNewPacked+otherNewPacked+recipe)
	return nil
}
