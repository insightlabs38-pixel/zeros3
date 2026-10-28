package main

import (
	"path/filepath"
	"testing"
)

// The report commands run end to end on the committed Roslyn fixtures.
func TestCommandsOnFixtures(t *testing.T) {
	fx := func(n string) string { return filepath.Join("..", "..", "clrz3", "testdata", "fixture-"+n+".dll") }
	r0, body, early := fx("r0"), fx("body"), fx("earlytype")
	for name, args := range map[string][]string{
		"scan":    {r0},
		"attrib":  {r0, body, r0, early},
		"econ":    {"cdc,meta-sections,raw-unit-4k,o-all-4k,n-unit-4k-a12", r0, body, early},
		"lockin":  {r0, body, early},
		"rolling": {r0, body, early},
		"reads":   {r0},
		"perf":    {r0},
		"cover":   {r0, body, early},
		"prove":   {r0, body, early},
		"refc":    {r0},
		"locals":  {r0},
		"diffc":   {r0, early},
	} {
		if err := cmds[name](args); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := cmds["econ"]([]string{"-star", "-ref", "35", "cdc,n-unit-4k-a12", r0, early}); err != nil {
		t.Errorf("econ -star: %v", err)
	}
}
