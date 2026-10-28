# Z3-41 WIM/ESD native identity frontier

Decision: DEFER. Opaque native units are useful on controlled non-solid images;
solid full-reexport gaps do not justify a new codec without real-history evidence.
No related Windows image/update history is claimed. No production change.

## Producers and scope
24 native captures: none, XPRESS20, LZX20, solid-LZMS10; six tree states each.
Trees contain existing hash-pinned Q5 Go PE artifacts, stable random files, a
hard-link alias, binary update, file patch, append, metadata-only and delete/rename.
They are synthetic Windows-like trees with Unix metadata, not Windows OS images.
Nine multi-image states cover copied base, native append, then soft-delete for
none/LZX/solid-LZMS. Three bases duplicate existing bytes:33 state files,30 unique
SHA-256 inputs. Multi-image copies are not extra independent histories.
No large/proprietary Windows download or promotion is inferred from these controls.
Debian1.13.6 tool packages are pinned; actual executable/library report1.13.5.
Local extraction includes pinned NTFS/FUSE libraries; no mount is used at decode.
Capture/verify use one producer thread. All33 outputs pass native verification.

## Characterization and exactness
The isolated native lookup oracle inventories header/version/flags/GUID, resource
tables, XML image count, metadata hashes, native SHA-1 resource IDs, aliases,
compressed chunks and solid physical containers. Solid logical offsets are not
misread as file offsets. Physical containers are charged once, not once per stream.
Ordinary compressed resources retain offset-table bytes and opaque compressed
chunks; solid containers retain their header, chunk lengths and opaque payloads.
Unknown framing, XML, metadata, integrity and obsolete/unreferenced source bytes
remain exact through existing Concat. Soft deletion does not erase bytes merely
because the active resource table no longer references them.
Each root is serialized and proves SHA-256/length/byte equality and reachability;
no native executable or analysis closure is needed by that existing stdlib decoder.
Native SHA-1 identities are diagnostics only; SHA-256 remains object identity.
No candidate WIM parser, XPRESS/LZX/LZMS decoder or generic BASE was implemented.
Encode-time region positions are framing ORACLES, not a production analyzer claim.

165 rows give99 original-file and634 plaintext-file interior proofs (733 total).
Plaintext rows cover active file data only, omit metadata/codec decisions, and do
not claim reconstruction of compressed WIM/ESD bytes. No canonical case is dropped.
Source/hash/span negative checks fail closed. Input128MiB in the shared harness;
all actual files<=16MiB; oracle decoded declarations<=128MiB and chunks<=65536.

## Six-state cumulative packed estimates, bytes
Shared estimator: production DEFLATE threshold +64B/object, full serialized roots
and reachable opaque components. These are estimates, not production disk savings.
|Profile|CDC|Opaque resources|Opaque chunks|CDC + chunks|
|---|---:|---:|---:|---:|
|None|4,352,920|4,624,240|4,624,240|8,977,160|
|XPRESS|5,007,256|4,673,172|4,227,804|9,235,060|
|LZX|4,679,615|4,340,959|3,909,513|8,589,128|
|Solid LZMS|10,165,498|10,633,596|10,633,574|20,799,072|

Plaintext-file production-CDC oracle4,025,764 B;4KiB oracle4,209,357 B.
XPRESS/LZX chunks approach that tested file-data oracle without codec emulation.
Standalone LZX17%/XPRESS16% improvements remain additive with authoritative CDC.
Solid reexports share native file identities while a changed packed container
rewrites many bytes: a large logical gap exists, but replay-decision cost is unknown.
There is no real-history evidence here to justify building that difficult codec.

## Multi-image and integrity findings
Native append reuses resources already present; a solid multi-image history costs
4,058,690 B CDC versus3,832,061 B opaque chunks, near the same file-data oracle.
Multi-LZX costs4,481,942 B CDC versus4,313,076 B chunks; multi-none4,067,282 versus
4,619,390 B. Native sharing already performs much of the desired lineage work.
The integrity table is independently SHA-1 checked for every state: coverage is
[header208,end of current lookup table), in native-sized blocks. Current XML and
header are outside that coverage; generic whole-object SHA-256 proves their bytes.
XML image counts1→2→1 match append/soft-delete. Obsolete bytes stay reconstructible.

## Cold reads, resources, gates and continuation
297 fresh-source4KiB samples are exact and touch only reachable dependencies.
Logical source amplification maxima: XPRESS17.79x/LZX17.74x; raw whole-resource
1072.05x and solid532.22x. Multi-image maxima are separately retained. These are
logical CAS request counts including roots, not physical I/O or semantic path reads.
The existing bounded Concat range helper is reused; no public S3 policy changes.
Entire proof/price/range characterization6.25s,130.5MiB RSS (compiler excluded).
Full root/lab tests and vet pass; no new decoder/concurrent surface needs new race
or fuzz work. The native header/lookup extraction remains isolated oracle code.
Checkpoints08715fc corpus/identity characterization; final adds append/delete,
independent integrity coverage, full costs, negative checks and this report.
Artifacts: testdata/q13-wim; reproduce q13_produce.py, q13_multi.py,
q13_inventory.py, cmd/z3wimfrontier, q13_measure.py, q13_validate.py/q13_summarize.py.
Native GUID/time fields may vary on regeneration; recorded output hashes bind
these measurements. Valid cached captures are hash-checked and never redownloaded.
Continue Q14 from latest main; production drafts80/81 stay unmerged.
