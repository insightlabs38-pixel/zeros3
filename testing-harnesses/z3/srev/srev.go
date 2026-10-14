// Package srev is the Z3-15F candidate for a shared revision shape. It exists
// to be tested against the CAR, Merkle, EraE and synthetic-snapshot adapters,
// not to be a framework: every field below is something at least one adapter
// needs, and the experiments report which ones more than one adapter used.
package srev

import "io"

// Component is one reusable byte string. Native is an opaque identity in the
// adapter's own hash domain (a CID, a node hash, ...); SHA is ZeroS3's own
// identity of the exact bytes and is the only thing storage keys on.
type Component struct {
	Native []byte
	SHA    [32]byte
	Size   int64
	Data   func() ([]byte, error)
	Class  string
}

// Member places a component in a revision. Via means the component is reachable
// through another member (a parent node or manifest lists its identity), so the
// recipe carries no reference to it.
type Member struct {
	C   Component
	Via bool
}

// Revision is what an adapter hands to the storage accounting.
type Revision struct {
	Roots [][]byte // native identities of the revision's roots (opaque)
	Bases [][]byte // native roots of the revision(s) this one was derived from
	// Members in the order the recipe lists them. Components reused from earlier
	// revisions that are reachable only through members need not be listed.
	Members []Member
	// Residual is the exact recipe content that is not a component reference:
	// headers, wrapper, order, derived-value switches. It is charged as is.
	Residual int64
	// Index is the native-to-ZeroS3 lookup added for this revision (bytes).
	Index int64
	// Rebuild reproduces the exact outer bytes, fetching components by SHA.
	Rebuild func(w io.Writer, get func(sha [32]byte) ([]byte, error)) error
	// Notes is free-form adapter detail for reports.
	Notes map[string]any
}
