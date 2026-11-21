# Whole-pack parity on the Coreutils archive store

The production CLI covered the 60,306,711-byte pack from a previous
depth-two Coreutils archive migration. The sidecar used 8,390,544 bytes,
or 13.9% of the pack. The isolated clone was damaged in three separate
stripes at the recorded offsets. The restored output matched the source
pack byte for byte and by independently calculated whole-file SHA-256.
After installing the checked file in the clone, a fresh process passed
deep verification of all three authoritative bodies. The original store
was the read-only input.

This trial restored three damaged blocks, one per stripe. The focused
test also checks multiple damaged blocks in a stripe and rejects a
corrupt sidecar without exposing a recovered file.

A separate run used the locked in-store installer on a new clone. The
backup matched the damaged input, the installed pack matched the original
and a fresh process passed deep verification. The process-cut test stops
after the durable backup and before rename, then reruns installation.
