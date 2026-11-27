# GF5 lossless checkpoint windows

Use the published EleutherAI Pythia-14M steps 100000, 101000 and 102000.
The first two establish candidate prices. The last target is held out
from codec selection and remains useful as a rolling prediction test.
Use exact serialized safetensors, including original data offsets,
tensor ordering, metadata bytes and file digests.

Compare ordinary CDC, native numeric authority, tensor-name grouping,
BitX-style XOR residuals, integer-delta coding, and a reversible
multi-anchor and tile residual search. Keep arithmetic in the original
word-payload integer ring. Preserve all NaN, infinity and signed-zero
encodings, exact metadata and unsupported data. Include reordered
tensors, random initialization, width changes and unprofitable layouts.

Physically pack complete closures with bounded windows and verify exact
whole reads, tensor ranges and base retention. Charge full copies where
a representation references a historical anchor. Measure actual startup,
base bytes read, build CPU and allocation. Reuse the production
safetensors parser and numeric incumbent. A generated negative control
cannot substitute for an independently trained checkpoint.
