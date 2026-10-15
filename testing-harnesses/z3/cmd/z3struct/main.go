// Command z3struct runs the Z3-18 structured-data shootout.
//
//	z3struct chars  [-lane sqlite|leveldb|rocksdb|parquet|zarr|all]   Phase-1 characterisation (controls + native-boundary oracles)
//	z3struct sqlite                                                    promoted-candidate evidence: sqlitez3 page tree vs controls
//
// Corpora are produced outside Git by oracle/structured/*.py into $Z3_STRUCT_CACHE
// (default ~/.cache/z3-realworld/structured). Nothing here is a production API.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3struct <chars> [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "sqlite":
		err = cmdSQLite(os.Args[2:])
	case "chars":
		err = cmdChars(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "z3struct:", err)
		os.Exit(1)
	}
}
