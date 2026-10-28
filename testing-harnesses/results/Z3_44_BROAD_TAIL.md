# Z3-44 broad tail — selection checkpoint

Q16 remains active; production untouched. Five selected formats were characterized
before choosing one deep target: SQLite WAL, WOFF2/Brotli, Avro, ONNX and 7z.

44 native inputs cover 12 committed WAL captures (append/checkpoint/independent),
three real Font Awesome releases plus two same-input Brotli reexports, twelve
native Avro states, six native edits/reexports of public SqueezeNet, and nine
native archives of three real hiredis releases. Explicit producer workloads are
not natural histories. Every captured WAL/base pair reopens and matches native
SQLite integrity/query checks; native font transforms, Avro rows, ONNX checker
and 7z extraction/test independently validate their respective producer outputs.

Broad Stage-A prices include exact source CDC, fine CDC, native-boundary oracle,
logical-component lower bounds and additive source closures. Logical-only views
omit container/coding decisions and never claim exact outer reconstruction.

ONNX is selected as the ONE deep target. Initializer reordering costs 1,568,158 B
under CDC versus 5,770 B under native boundaries. Six-state CDC is 6,569,156 B
versus native 4,977,942 B. Typed tensor identity, existing Model/Concat primitives
and bounded protobuf framing give the strongest low-cost mechanism.
WAL page boundaries help independent replays but lose badly across checkpoints.
WOFF2 Brotli replay would require a new decision codec; Avro/7z have weaker gaps.

A typed ONNX framing experiment now extracts only graph dense raw initializers,
retains unknown protobuf fields exactly, and does not follow external tensor paths.
It reuses Q12 region/plane composition; no generic protobuf normalization or new
production kind. Initial exact proofs pass; native metadata parity, malformed-input
fuzzing, final prices/ranges/resources and decisions remain to close.
