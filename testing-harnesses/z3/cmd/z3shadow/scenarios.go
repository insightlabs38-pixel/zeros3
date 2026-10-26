package main

import (
	"fmt"
	"path/filepath"

	"zeros3-z3/prodshadow"
)

// scenarios prints the failure matrix, the GC lifecycle and the end-to-end lifecycle.
func (c config) scenarios() {
	pages := 600
	if c.full {
		pages = 1500
	}
	for _, s := range []struct {
		name string
		run  func(string) ([]prodshadow.Row, error)
	}{
		{"failure matrix", func(w string) ([]prodshadow.Row, error) { return prodshadow.RunFailureMatrix(c.b, w, pages) }},
		{"catalog loss", func(w string) ([]prodshadow.Row, error) { return prodshadow.RunCatalogLoss(c.b, w) }},
		{"GC", func(w string) ([]prodshadow.Row, error) { return prodshadow.RunGC(c.b, w, pages) }},
		{"lifecycle", func(w string) ([]prodshadow.Row, error) { return prodshadow.RunLifecycle(c.b, w, pages) }},
	} {
		fmt.Println("==", s.name)
		rows, err := s.run(filepath.Join(c.work, "sc-"+s.name))
		must(err)
		for _, r := range rows {
			fmt.Println(r)
		}
	}
}
