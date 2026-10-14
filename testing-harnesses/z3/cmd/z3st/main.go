// z3st reports the Z3-09 Safetensors characterisation: tensor identity, bit-level
// change statistics, checkpoint-spacing curves, sub-tensor blocks, simple exact
// deltas, tensor classes, header churn, re-sharding and base selection, over
// checkpoint families laid out by `z3fetch -out` (real) or `z3st reshard`
// (constructed from real tensors). Everything is laboratory evidence.
package main

import (
	"fmt"
	"os"
	"path"
	"strings"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: z3st inspect|identity|bits|spacing|blocks|delta|class|header|base|cast|reshard -corpus DIR [-families g,..] [flags]`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "inspect":
		inspectCmd(args)
	case "identity":
		identityCmd(args)
	case "bits":
		bitsCmd(args)
	case "spacing":
		spacingCmd(args)
	case "blocks":
		blocksCmd(args)
	case "delta":
		deltaCmd(args)
	case "class":
		classCmd(args)
	case "header":
		headerCmd(args)
	case "base":
		baseCmd(args)
	case "cast":
		castCmd(args)
	case "reshard":
		reshardCmd(args)
	default:
		usage()
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "z3st:", err)
	os.Exit(1)
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func matchAny(globs []string, name string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, g := range globs {
		if ok, _ := path.Match(g, name); ok {
			return true
		}
	}
	return false
}

func pct(a, b int64) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", 100*float64(a)/float64(b))
}

func pct2(a, b int64) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%.2f", 100*float64(a)/float64(b))
}
