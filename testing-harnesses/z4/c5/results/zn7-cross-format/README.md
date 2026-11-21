# Exact SmolLM cross-format model selection

A pinned SmolLM safetensors file and its llama.cpp F16 GGUF export were
written through signed S3. The two migrations used archive policy with
structural representations disabled. One used exact tensor names and
the other also considered llama.cpp aliases when GGUF metadata declares
the llama architecture. The encoder still proved every derived window
and checked the whole target. The ZN7 wire format stayed the same.

All lanes used the same GC, repack, compact and recompression sequence.

| Lane | Retained allocated bytes | ZN7 records |
| --- | ---: | ---: |
| CDC | 528,154,624 | 0 |
| Exact-name selector | 528,154,624 | 0 |
| Llama aliases | 288,403,456 | 1 |

The recovered 270,885,952-byte GGUF and its midpoint 4 KiB read matched
the original bytes. Both objects passed independent digest comparisons
and deep verification in all three lanes. The archive migration took
about 70 seconds in the alias lane under shared load. Its GGUF whole
read took 7.40 seconds versus 1.71 seconds in CDC. The two individual
midpoint reads were 5.28 ms and 2.68 ms respectively. The fixture shows
a storage benefit and a whole-read cost. It is a single pair, with one
read per lane, and establishes no general throughput result.

[Manifest and measurements](trials.json)
