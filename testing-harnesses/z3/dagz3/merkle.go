package dagz3

import "zeros3-z3/merklez3"

// merkleHash is the native hash of the lab's Merkle workloads: not SHA-256, so the
// representation carries owning record sections (the interesting VIA case).
var merkleHash = merklez3.HashSHA3_256
