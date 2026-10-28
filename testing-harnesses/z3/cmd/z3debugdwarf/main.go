package main

import (
	"encoding/json"
	"os"
	"zeros3-z3/debugsym"
)

func main() {
	for _, path := range os.Args[1:] {
		b, e := os.ReadFile(path)
		if e != nil {
			panic(e)
		}
		s, e := debugsym.SnapshotDWARF(b)
		if e != nil {
			panic(e)
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{"path": path, "snapshot": s})
	}
}
