# Shipped executable histories

The six ELF histories use `go`, `asm`, `cgo`, `compile`, `link` and
`vet` as distributed in Go 1.27.1 through 1.27.4 for Linux amd64. The
probe uses these files as storage inputs. It makes no runtime or compiler
behavior claims. A separate B4 graph run used five published Newtonsoft
PDBs. It replayed them in a fresh process and an independent Python
oracle matched all five release digests.

`z3compose exec-history -json FILE...` measures the charged CDC and
standalone EXR1 costs over all four chronological release states. Each
charged recipe reconstructs exact bytes. Under the laboratory pack rule,
the six standalone EXR1 histories project 19.0% to 23.2% lower charged
cost than CDC.

`z3bin exact-delta -base FILE -cur FILE -out NEW_DIR` writes a compressed
base and encoded delta for each of the raw and normalized paths. Both
paths use the same 1 MiB-frame windowed delta implementation. The
normalized path includes every stream and its framing. Replay reopens
and verifies the stored base, reads the stored delta and independently
checks the complete executable and its sample range.

| ELF family | Raw base + delta files | Normalized base + delta files | Lower file bytes |
|---|---:|---:|---:|
| asm | 2,563,206 | 2,178,544 | 15.0% |
| cgo | 2,349,321 | 2,065,418 | 12.1% |
| compile | 12,732,104 | 10,099,187 | 20.7% |
| go | 9,886,613 | 8,581,730 | 13.2% |
| link | 3,852,133 | 3,365,202 | 12.6% |
| vet | 4,441,455 | 3,910,648 | 12.0% |

The compile pair showed the largest file-byte improvement. Its 4 KiB
sample took about 150 ms with normalization and 2.2 ms with raw delta.
The normalized path rebuilds the whole executable for the range sample.
These experimental file bytes include stored base and delta metadata.
Production namespace, GC and read costs remain to be measured for an
adoption decision. The B4 PDB run covers published symbol data separately.

[Source digests, charged costs and independent replay data](results/shipped-executable-histories.json)
