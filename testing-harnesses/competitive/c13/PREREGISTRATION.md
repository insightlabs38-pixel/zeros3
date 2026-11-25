# C13 · Shared compression context for small objects

Compare finalized C12 native CDC and grouped authority records with a
matched independent-record archive. Reuse C6's measured placement and
restartable-block results as the negative reference. Price payload,
dictionary and index allocation, directory slack, aliases, and update
amplification separately.

Test two mechanisms. First compare per-object deflate with scope-specific
prefix dictionaries and matched zstd with and without trained cohort
dictionaries. Training uses earlier data and the scope binds dictionary
identity. Second test shared, independently decodable 32 KiB cohort blocks
with a bounded object index and copy-on-write generations. That mechanism
compresses across object boundaries inside each block.

Use a deterministic mixture of small events, release source/config
fragments, SQLite and volume pages, model metadata and independent bytes.
Freeze separate training and evaluation identities. Keep an independent
held-out release source family until the mechanisms are frozen. Verify the
exact digest for every live alias and distinguish fresh process reads from
an uncontrolled kernel cache. Compare one-client sparse/whole reads and
eight-client throughput, aggregate encoding/training CPU, RSS, staged
bytes and allocated storage before and after churn.

Verify dictionary hash, ownership, reference counts, missing/corrupt
dictionaries, old readers across replacement, journal-style atomic
publication before retire, and exact reopen. Keep prototypes separate
from production unless a clear storage/read/CPU frontier justifies
an integrated format and full production closure verification.

After the first main pilot, add a fixed 8 KiB block control to test the
read-amplification tradeoff observed with 32 KiB blocks. Use the same
block format, namespace and frozen held-out evaluation.
