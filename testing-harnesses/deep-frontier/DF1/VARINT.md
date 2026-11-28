# Canonical varint validation

The standard library still decodes the exact byte sequence. A nonpositive
consumption count is refused before canonicality or value-limit checks.
For a successful base-128 decode with value `v`, the shortest encoding
takes `ceil(bit_length(v)/7)` bytes, with one byte for zero.
`bits.Len64(v|1)` preserves the bit length of every nonzero value and
supplies one for zero. The calculated width therefore equals the length
of the standard-library re-encoding on all uint64 values. Canonicality
still precedes the configured limit check. Position and error behavior
are unchanged.

The focused test compares the original criterion against the new reader
over every one- and two-byte input, every seven-bit transition, tenth
byte overflow and overlong cases, truncations, nonzero offsets and
limits on either side of accepted values. It also checks 40,000
deterministic randomized sequences, sticky errors and zero allocations
on accepted input.
