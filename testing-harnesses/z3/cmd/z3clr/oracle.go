package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"

	"zeros3-z3/clrz3"
)

func init() { cmds["oracle"] = oracleCmd }

// oracle FILE : compare the output of oracle/clrwasm/ClrRows (the .NET metadata reader, ORACLE
// only) with clrz3's own parse: every table's row count, the heap sizes, and the body count, IL
// bytes, exception regions and local-signature tokens summed over all method bodies.
func oracleCmd(args []string) error {
	fh, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n, bad := 0, 0
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "\t")
		if len(parts) < 4 {
			continue
		}
		path := parts[0]
		d, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := clrz3.Parse(bytes.NewReader(d), int64(len(d)), clrz3.Limits{})
		if err != nil {
			fmt.Printf("%s: our parse failed: %v\n", path, err)
			bad++
			continue
		}
		want := map[int]int64{}
		for _, kv := range strings.Split(parts[1], ",") {
			var t int
			var r int64
			fmt.Sscanf(kv, "%d=%d", &t, &r)
			want[t] = r
		}
		ok := true
		for t := 0; t < clrz3.NumTables; t++ {
			if want[t] != int64(f.Rows(t)) {
				fmt.Printf("%s: %s rows %d, oracle %d\n", path, clrz3.TableName(t), f.Rows(t), want[t])
				ok = false
			}
		}
		var str, blob, us, guid int64
		fmt.Sscanf(parts[2], "str=%d blob=%d us=%d guid=%d", &str, &blob, &us, &guid)
		// the reader reports #Strings without its 4-byte alignment padding
		if str > f.Str.Size || f.Str.Size-str > 3 || blob != f.Blob.Size || us != f.US.Size || guid != f.GUID.Size {
			fmt.Printf("%s: heap sizes %d/%d/%d/%d, oracle %d/%d/%d/%d\n", path, f.Str.Size, f.Blob.Size, f.US.Size, f.GUID.Size, str, blob, us, guid)
			ok = false
		}
		var bodies, il, eh, loc, withLoc int64
		fmt.Sscanf(parts[3], "bodies=%d il=%d eh=%d locals=%d withLocals=%d", &bodies, &il, &eh, &loc, &withLoc)
		var gb, gil, geh, gloc, gwl int64
		for _, b := range clrz3.FindBodies(f, d, false) {
			if b.Opaque {
				continue
			}
			gb++
			gil += int64(b.Body.CodeLen)
			geh += int64(len(b.Body.Clauses))
			if b.Body.LocalTok != 0 {
				gloc += int64(b.Body.LocalTok & 0xffffff)
				gwl++
			}
		}
		if gb != bodies || gil != il || geh != eh || gloc != loc || gwl != withLoc {
			fmt.Printf("%s: bodies %d/%d IL %d/%d EH %d/%d locals %d/%d (%d/%d) vs oracle\n", path, gb, bodies, gil, il, geh, eh, gloc, loc, gwl, withLoc)
			ok = false
		}
		if !ok {
			bad++
		}
		n++
	}
	fmt.Printf("oracle: %d assemblies compared, %d differ\n", n, bad)
	if bad > 0 {
		return fmt.Errorf("%d assemblies differ from the oracle", bad)
	}
	return nil
}
