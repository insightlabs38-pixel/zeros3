# GF5 · exact tensor checkpoint windows

Native ZN7 retained the least space on three genuine Pythia-14M training
checkpoints. The proposed history transforms improved over generic
and single-transform lanes. They add no candidate to the native frontier
on this evidence.

## Corpus and method

The pinned training steps 100000, 101000 and 102000 are genuine public
training outputs from the same model. Each file has 76 F16 tensors and
28,143,920 bytes. Step 102000 is a held-out target with the same frozen
per-window search. The production Safetensors parser verifies named
tensor geometry. Matching requires the same name, dtype and shape.
The exact original header, data order and each output SHA are retained.
[Public model card](https://huggingface.co/EleutherAI/pythia-14m) · [Exact pins](inputs.json)

Independent 64 KiB windows compare direct bytes, XOR residual, modular
integer residual, independent sign/exponent/mantissa residual planes,
row lifting, two-anchor extrapolation, sparse word changes and existing
FPE for independent anchors. All arithmetic uses integer bit patterns.
A portable transposition and native pack-run reads remove avoidable
decode overhead. Physically repack the closure of the last checkpoint,
retaining older records only for its verified frame dependencies.

| Three checkpoints | Allocated bytes | Whole probe p95 | 4 KiB p95 | Last checkpoint alone |
| --- | ---: | ---: | ---: | ---: |
| Native CDC | 84,594,688 | 72 ms | 0.29 ms | — |
| Native ZN7 | 63,791,104 | 331 ms | 2.41 ms | — |
| Packed direct | 84,566,016 | 53 ms | 0.19 ms | 28,229,632 |
| BitX-style XOR | 81,326,080 | 140 ms | 1.11 ms | 35,254,272 |
| Integer-delta | 79,478,784 | 195 ms | 0.99 ms | 38,592,512 |
| Adaptive residuals | 73,236,480 | 645 ms | 1.73 ms | 73,228,288 |
| Adaptive with FPE anchors | 69,058,560 | 833 ms | 2.09 ms | 65,421,312 |

Whole probe p95 is descriptive with one sample per checkpoint. There
are 54 sparse probes per lane. The pack experiment charges actual
record and index blocks. The native lanes also include live namespace
and journal accounting. Independent zstd-6 used 77,688,832 allocated
bytes, and zstd patch-from used 77,697,024 across both required ancestors
and the target. These reference files have no ZeroS3 metadata.

ZN7 admitted two targets with about 6.6 s planning CPU. Adding FPE to
the adaptive lane cut 4,177,920 bytes, but the combined lane still cost
8.3% more than ZN7 and had a slower whole restore. Row lifting won no
windows. The two-anchor mode won 21 held-out windows. Its physical
charge matters after prune: the final selected checkpoint still required
65.4 MB, 2.3× a self-contained checkpoint. Near-optimal coding with
these fixed dependencies cannot remove that retention cost. Better
window selection needs to price the retained basis and whole reads.

Generated controls verify reordered tensors, F16 and BF16 fallback,
independent reinitialization, exact NaN payloads and signed zero, and
small, named optimizer moments. These states are controls. The
published training panel contains exact weights, not optimizer moments.
A focused race run and 3,000 malformed-decoder inputs passed.

[Reproduction](REPRODUCE.md) · [Evidence](results/) · [Design](PREREGISTRATION.md)
