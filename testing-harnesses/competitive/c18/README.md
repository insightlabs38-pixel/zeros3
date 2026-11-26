# ZeroS3 C18 · Verified local repair and portable recovery

**C18_RECOVERY_EFFECTIVE.** Deep verification now reports a damaged
hidden preset copy while ordinary reads can use its twin. An explicit
offline repair restores the original pack from a single known-good
copy. The same logical workflows passed on amd64 and emulated arm64.

## Dictionary repair economics

A 6,317,789-byte pack from C13 carries 4,926 distinct source fragments
in the surrounding store. Each trial flipped a byte in one dictionary
copy and verified 96 independent signed object reads before and after.
The old deep check observed only the successfully reconstructed
objects. The new check hashes both hidden contexts and reports the
failed physical copy.

| Recovery lane, two trials | End-to-end wall | CPU | Verified full physical health |
| --- | ---: | ---: | --- |
| Targeted repair and deep verify | 0.289 s | 0.394 s | Yes |
| Ordinary recompress and reclaim | 0.317 s | 0.511 s | No, skipped already-compressed pack |
| Forced repack using original preset and deep verify | 1.294 s | 2.244 s | Yes |

Both successful repair paths retained 8,208,384 allocated bytes and
restored the original pack SHA-256. Targeted repair replaced an
8,236- or 8,240-byte record inside one complete atomic pack rewrite.
It wrote the same order of physical bytes as forced repack and avoided
recompression. Post-repair signed p95 ranged from 8.8 to 11.2 ms in
the initial pair. Those short trials do not establish a tail advantage.

Use `doctor -deep -repair-pack-dictionaries` under exclusive access.
The repair builds a private file with the indexed original record,
checks the complete original pack identity and every decoded record,
fsyncs and renames. A serving process prevents it from taking the lock.
A fault in both copies or other pack payload bytes leaves the original
untouched. The copies share one pack and cannot recover loss of the file.

## Recovery and consumers

The native amd64 run passed 22 operations, including Boto3 range,
multipart and retry behavior, the official AWS SDK for Go v2, Restic
S3 backup and exact restore of two revisions, portable snapshot
bundles, authority revert, peer repair and acknowledged-write restart.
The static Linux/arm64 build passed the 19-operation Boto3 and recovery
smoke under QEMU. It matched the native object, repaired chunk and
acknowledged-object digests. The two separate emulated dictionary
repairs restored the same original pack SHA and deep verified. QEMU
numbers describe the emulator.

Child processes were killed immediately before and after the repair
rename. Retry either performed the verified repair or found the already
repaired pack. Focused and race suites covered corrupt copies, unrelated
payload damage, stale index generation and derived import recovery.
The focused suite also repeated the integrated journal watermark cuts.
These are local process-loss and restart findings. Physical power loss,
independent-media loss, native ARM performance and large deployment
qualification remain open.

[Failure map](FAILURE_MAP.md) · [Reproduction](REPRODUCE.md) · [Raw rows](results/)
