package gitz3

import (
	"zeros3-z3/lab"
	"zeros3-z3/srev"
)

// RevisionOf expresses an exact recipe in the Z3-15F shared shape (see
// lab.RevisionOfPlan). The Z3-16 review asks whether anything beyond those
// ingredients was needed: it was not - Roots and Bases stay empty, because a Git
// pack's roots (refs) are not stored and its delta bases are descriptor content,
// not separate revisions.
func RevisionOf(pl *lab.Plan) *srev.Revision { return lab.RevisionOfPlan(pl) }
