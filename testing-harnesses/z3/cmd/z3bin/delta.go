package main

import "zeros3-z3/pdelta"

// The pairwise delta moved to package pdelta (Z3-32); these are the old names.
type DeltaStat = pdelta.Stat

func deltaCost(base, cur []byte) DeltaStat            { return pdelta.Cost(base, cur) }
func deltaRuns(base, cur []byte, keep bool) DeltaStat { return pdelta.Runs(base, cur, keep) }
