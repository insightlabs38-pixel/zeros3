# Z3-12 — Reversible binary reference normalization + ELF

Laboratory result. Nothing here touches `zeros3.go`, the root module, a persistent format or any production path.
Code: `binref/` (format-independent substrate), `elfz3/` (bounded ELF64 parser), `gopcln/` (Go function table reader),
`cmd/z3bin` (corpus builder + measurements). Reproduce: `z3bin corpus -out DIR -goos linux -goroot COPY_OF_GOROOT`, then
`z3bin report -corp DIR -s matrix|ident|attr|debug|meta|perf|pie|meta-flags|xor`.

## A. What was built

```
exact file bytes
  └─ Image { Regions (exact partition of the file), Syms, Slots (listed reference fields) }
        ├─ ELF parser:   header tables, one region per file-backed section, gaps, NOBITS as zero-length
        │                virtual regions, symbols, .rela/.rel slots, symtab st_value slots
        ├─ gopcln:       Go function table (present even when stripped): function starts, names, 32-bit
        │                text-relative entry offsets
        └─ x86 scan:     CALL/JMP/Jcc rel32 and RIP-relative operands (incl. immediate-bearing, VEX/EVEX forms)
Normalize(image, slots, units, identity) → streams:
   regions table | units table | payload per region (slot bytes zeroed) | reference stream per region
Reconstruct(streams) → original bytes          (no access to the Image or the parser)
```

A slot is `{offset, encoding (rel32+bias | abs64 | abs32 | rva32 | text-offset32), class}`. The *target* is decoded from the
slot's own bytes, so `encode(decode(v)) == v` is checked per slot and the exact reconstruction does not depend on what
the scan believed the instruction was: a false positive costs one wasted reference record, never correctness.
A reference is recorded as `(slot delta, encoding, target unit id, offset in unit)` or, when no unit contains the target,
`(region, offset in region)`. Intra-function targets are left raw (their displacement does not move with the function).
Targets whose identity is ambiguous (two units with the same id) are left raw. Units come from the symbol table, from the
Go function table, or (no names at all) from CALL targets.

Every normalized row reported below was rebuilt from its streams and compared by length, SHA-256 and bytes
(`normRep` aborts otherwise). The ELF parser rejects: header/phdr/shdr tables outside the file, section ranges that leave
the file or overflow, bad section-name index, string indexes outside their table, unterminated or over-long names, symbol
tables with wrong entry size or link, relocation sections with wrong entry size or link, relocations naming a symbol past
the table. ELF32 / big-endian are `ErrUnsupported`. Everything else stays in opaque regions.

## B. Corpus

Built by the Go toolchain from this module's own `cmd/z3lab` (the real 5-6 MiB Go ELF of the Z3-07 evidence) with
`go build -overlay` (the working tree is never edited): `go version go1.27.0 linux/amd64`, `CGO_ENABLED=0`.
Builds are byte-reproducible (r0, r2, early rebuilt in a second directory: `cmp` identical); SHA-256 and sizes are written to
`<goos>-<arch>-provenance.json` by the builder. Nothing is committed.

| revision | source change (one file in package `main`, kept alive by `init`) |
|---|---|
| r0 | base: two functions, a string and a numeric constant |
| r1 | string and constant changed |
| r2 | + `fnNew` (about 56 bytes of machine code) |
| r3 | + different body for `fnB` |
| early | r2 + a function in package `errors`, which links before nearly everything: all later addresses move |
| swap | r2 with `fnNew` emitted before `fnA` (layout movement inside the package) |
| data | r2 + a 96-entry global table |
| deps | r2 + `encoding/csv`, `text/tabwriter` (new packages mid-link) |

Flag sets (each is its own family, never mixed in one pair): `std` (`-trimpath`), `strip` (`-ldflags=-s -w`), `notrim`,
`nobid` (`-ldflags=-buildid=`), `pie` and `piestrip` (`-buildmode=pie`: explicit `.rela` relocations). Sizes: std about
6.29 MB (2.04 MB `.text`, 1.58 MB `.gopclntab`, 1.76 MB compressed DWARF, 0.14 MB `.symtab`), strip about 4.22 MB.
`linux/arm64` std/strip is built as characterization. A small gcc 13.3 and clang 18.1 static glibc C family (`z3bin ccorpus`:
c0 base, c1 one function inserted first in the object, c2 constant change) checks that nothing below is Go-specific.

## C. Structural baseline (question 1)

Percent of the *current* build that is new bytes (new CDC chunk bytes, production CDC v1, each distinct chunk once).

#### linux/amd64 std: new bytes as % of the current build

| pair | raw CDC | fixed 4K | section | sect+CDC | func ranges | reloc+struct | x86 refs | full | ORACLE | CDC-4K raw | CDC-4K full | Δ raw | Δ full |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| const+string r0→r1 | 77.1 | 97.8 | 96.0 | 76.7 | 68.1 | 78.2 | 52.3 | 51.3 | 41.8 | 66.5 | 36.5 | 28.2 | 26.5 |
| add function r1→r2 | 81.1 | 90.0 | 99.9 | 81.2 | 69.3 | 82.9 | 56.8 | 55.8 | 46.5 | 65.2 | 33.4 | 28.5 | 26.4 |
| change body r2→r3 | 45.1 | 44.8 | 87.5 | 37.2 | 35.6 | 38.4 | 37.2 | 39.9 | 38.4 | 28.5 | 28.0 | 26.0 | 26.0 |
| remove function r2→r1 | 81.1 | 90.0 | 99.9 | 81.2 | 69.3 | 82.8 | 56.8 | 55.8 | 46.5 | 65.2 | 33.4 | 28.4 | 26.4 |
| early insert r2→early | 88.7 | 98.7 | 100.0 | 88.6 | 85.9 | 90.7 | 69.7 | 73.0 | 63.4 | 72.7 | 47.9 | 31.2 | 25.9 |
| reorder r2→swap | 43.9 | 33.9 | 90.5 | 38.5 | 36.8 | 38.7 | 40.9 | 42.9 | 38.7 | 26.1 | 26.0 | 23.9 | 23.9 |
| data table r2→data | 74.9 | 70.4 | 93.7 | 70.9 | 61.0 | 70.7 | 49.6 | 52.2 | 42.6 | 58.9 | 30.5 | 20.5 | 19.1 |
| new packages r2→deps | 90.7 | 95.6 | 99.9 | 93.9 | 89.8 | 95.5 | 74.9 | 77.7 | 68.2 | 78.6 | 54.4 | 34.0 | 29.6 |

#### linux/amd64 strip: new bytes as % of the current build

| pair | raw CDC | fixed 4K | section | sect+CDC | reloc+struct | x86 refs | full | ORACLE | CDC-4K raw | CDC-4K full | Δ raw | Δ full |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| const+string r0→r1 | 69.5 | 96.7 | 98.4 | 71.0 | 72.2 | 33.5 | 31.6 | 19.7 | 58.7 | 14.0 | 3.6 | 1.3 |
| add function r1→r2 | 74.2 | 85.1 | 99.9 | 75.4 | 77.4 | 36.9 | 34.9 | 24.4 | 55.7 | 6.5 | 3.4 | 0.3 |
| change body r2→r3 | 21.4 | 17.8 | 85.8 | 12.2 | 13.9 | 12.2 | 16.1 | 14.6 | 2.3 | 2.0 | 0.1 | 0.1 |
| remove function r2→r1 | 74.2 | 85.1 | 99.9 | 75.4 | 77.4 | 36.9 | 34.9 | 24.4 | 55.7 | 6.5 | 3.4 | 0.3 |
| early insert r2→early | 88.4 | 98.1 | 99.9 | 88.3 | 90.8 | 64.6 | 69.1 | 56.6 | 70.7 | 33.4 | 11.9 | 4.6 |
| reorder r2→swap | 18.2 | 1.6 | 85.8 | 13.8 | 13.0 | 17.6 | 19.6 | 14.2 | 2.3 | 2.3 | 0.1 | 0.1 |
| data table r2→data | 72.5 | 55.9 | 99.9 | 70.0 | 67.8 | 37.5 | 41.0 | 28.6 | 57.4 | 15.0 | 2.2 | 0.2 |
| new packages r2→deps | 87.9 | 93.5 | 99.9 | 90.9 | 92.8 | 61.9 | 65.7 | 53.3 | 73.4 | 36.1 | 9.8 | 3.4 |

Columns: `section` = one object per section (any change dirties the section); `sect+CDC` = CDC inside each section;
`func ranges` = one object per symbol-delimited function range (8.5 k objects, +96 B each not included here);
`reloc+struct` = ELF relocations, symbol values, Go function-table entry offsets masked; `x86 refs` = branch / RIP-relative
references only; `full` = all classes; `ORACLE` = payload streams only with **no reference metadata counted — a lower
bound, not an achievable cost**; `CDC-4K` = the same gear CDC with 1/16 of the production sizes (min 1 KiB, avg 4 KiB, max 16 KiB),
sensitivity only; `Δ` = pairwise copy/literal delta against the previous build (24-byte anchors, literals DEFLATEd, 6 B per copy)
over the raw file, or over the normalized payload plus reference streams.

**Plain structure does not help.** `sect+CDC` is within 1-2 points of raw CDC in every shifted-layout pair; whole-section objects
are 99.9% new; function ranges save 12 points on an add (81.1 → 69.3) but need 8.5 k objects, which at the lab's 96 B per reference
is another 13 points (82.3% total). Sections still contain every moved address.

## D. Explicit relocations (question 2)

* Static Go executables carry **no** relocation sections at all (`relocs 0`): the linker resolves everything. The `reloc+struct`
  column therefore only masks symbol values and Go function-table offsets, and is a net loss at production CDC (77.4 vs 74.2
  stripped add).
* `-buildmode=pie` has 17 505 `R_X86_64_RELATIVE` entries (data pointers). On the stripped PIE add-function pair they take CDC from
  41.4% to 28.7% (the best single class there); with the unstripped build the 1.6 MB of compressed DWARF hides it (59.9 → 48.9).
  Under address movement (`early`) relocations alone do nothing (90.0 → 92.1 CDC; Δ 10.2 → 10.0): code displacement, not data pointers,
  is the churn, and no relocation describes it.

##### -buildmode=pie (pie), add function r1→r2: 17505 ELF relocations (17505 usable slots)

| representation | CDC % | Δ % | meta B | refs |
|---|--:|--:|--:|--:|
| raw | 59.9 | 25.0 | 0 | 0 |
| reloc | 48.9 | 25.0 | 111146 | 17473 |
| x86 | 53.8 | 24.9 | 389094 | 53526 |
| full | 54.4 | 24.7 | 823319 | 123133 |

##### -buildmode=pie (piestrip), add function r1→r2: 17505 ELF relocations (17505 usable slots)

| representation | CDC % | Δ % | meta B | refs |
|---|--:|--:|--:|--:|
| raw | 41.4 | 0.8 | 0 | 0 |
| reloc | 28.7 | 0.8 | 109086 | 17495 |
| x86 | 34.8 | 0.7 | 358254 | 53376 |
| full | 34.4 | 0.6 | 744588 | 117200 |

##### -buildmode=pie (piestrip), early insert r2→early: 17507 ELF relocations (17507 usable slots)

| representation | CDC % | Δ % | meta B | refs |
|---|--:|--:|--:|--:|
| raw | 90.0 | 10.2 | 0 | 0 |
| reloc | 92.1 | 10.0 | 109104 | 17497 |
| x86 | 68.3 | 5.4 | 358304 | 53383 |
| full | 70.8 | 6.9 | 744707 | 117218 |

**Conclusion:** explicit relocations explain a small, data-pointer-shaped part of the churn. In linked executables the dominant
class is the one with no metadata: x86 branch and RIP-relative displacement.

## E. Instruction references (question 3) and the oracle

Stripped add-function: `reloc+struct` 77.4 → `x86 refs` **36.9** → `full` 34.9 (ORACLE 24.4). Std: 82.9 → 56.8 → 55.8 (ORACLE 46.5).
The pattern scan reaches almost everything in Go text: after masking, `.text` of the add-function pair differs in 97 bytes (raw: 31 409);
the early-insert pair in 55 bytes in 13 runs. How it got there is part of the result: the first scan (CALL/JMP/Jcc + plain RIP forms) left
50 k literal bytes in 4.7 k runs; adding BSS targets (NOBITS regions, a modelling gap, not a decoder gap), immediate-bearing forms
(`cmp/add/mov [rip+d], imm`), `setcc`, `0F 38` and VEX/EVEX moved it to 97 bytes. Residual unrecognised forms are AVX-512-specific code.
No disassembler was written: 150 lines of byte patterns with a target-must-be-mapped check, and the explicit slot list makes
imprecision harmless.

**Production CDC cannot cash the result in once the layout shifts.** For the stripped early insert the masked `.text` is identical to the
base except 13 runs / 55 bytes, yet 684 kB of its 2.04 MB (27 chunks) are new: each tiny residual dirties a 64 KiB-average chunk. The same data
under `CDC-4K` is 33.4% (add: 6.5%) and under pairwise delta 4.6% (add: 0.3%). **The binding constraint after normalization is chunk
granularity, not reference coverage.**

## F. Target identity (question 4) and layout movement (question 5)

Cells: production-CDC % / pairwise-delta %.

##### target identities, std (CDC % / pairwise-delta %)

| pair | units | ordinal | region-rel | symbol | content |
|---|---|--:|--:|--:|--:|
| add function r1→r2 | auto | 56.1 / 27.3 | 54.4 / 27.1 | 55.8 / 26.4 | 54.3 / 26.5 |
| early insert r2→early | auto | 73.0 / 27.3 | 71.3 / 26.9 | 73.0 / 25.9 | 96.3 / 27.5 |
| reorder r2→swap | auto | 39.7 / 23.9 | 40.2 / 23.9 | 42.9 / 23.9 | 38.9 / 23.9 |

##### target identities, strip (CDC % / pairwise-delta %)

| pair | units | ordinal | region-rel | symbol | content |
|---|---|--:|--:|--:|--:|
| add function r1→r2 | pcln | 34.8 / 0.4 | 34.9 / 0.4 | 34.9 / 0.3 | 37.0 / 0.4 |
| add function r1→r2 | call | 33.3 / 0.4 | 34.9 / 0.4 | n/a (no names) | 33.9 / 0.4 |
| early insert r2→early | pcln | 64.0 / 5.5 | 62.4 / 5.6 | 69.1 / 4.6 | 94.4 / 4.9 |
| early insert r2→early | call | 87.1 / 6.9 | 85.6 / 5.6 | n/a (no names) | 97.6 / 4.9 |
| reorder r2→swap | pcln | 15.5 / 0.1 | 15.6 / 0.1 | 19.6 / 0.1 | 16.2 / 0.1 |
| reorder r2→swap | call | 17.1 / 0.1 | 18.1 / 0.1 | n/a (no names) | 18.0 / 0.1 |

* **Symbol** (name hash + offset in unit) is the most stable where names exist: best delta under the early insert (4.6 vs 5.5-5.6).
* **Region-relative** (section + offset) is within 0.1 point of symbol for an append-at-the-end change (0.4 vs 0.3 stripped) and needs no names;
  under movement it ties ordinal (5.6 / 5.5) and trails symbol by one point.
* **Content** (hash of the target function's reference-masked bytes) survives movement in 99.8% of functions (4 201 of 4 209
  identical across the early insert) but loses CDC badly (94-97%): functions with identical bodies cannot be told apart by content, so
  their references stay raw and any raw reference left in text dirties every chunk around it. With no names at all (call-derived
  units, `call` row) content is the only usable id and it matches symbol in delta terms (4.9).
* **Ordinal** (control) loses to symbol under movement (5.5 vs 4.6) and is the worst identity when units are derived from CALL targets (6.9): rank shifts for every unit after an insertion, and spurious derived starts shift it further.
* **Reorder** (`swap`): unstripped Δ 23.9 → 23.9 (debug-bound); stripped 0.1 → 0.1 — a one-function reorder inside a package is already
  nearly free under delta and masking does not hurt it. Under production CDC masking gives nothing here (raw 18.2; 17.6 with x86 refs, 19.6 with all classes) because it adds a metadata stream to a layout that barely moved.
* **Insertion before everything** (`early`): the hard case. Stripped Δ 11.9 → 4.6, CDC-4K 70.7 → 33.4, production CDC 88.4 → 69.1.
  The remaining 4.6% is mostly references from code to *data* targets: without data symbols the target is only "section + offset", which
  shifts when data moves (Go's stripped binaries have function names via pclntab but no data symbols).

## G. Where the changed bytes are (question: change attribution)

Literal bytes of the current build against the whole base, raw versus after masking (`add function r1 → r2`).

##### attribution, add function r1→r2, std (literal bytes vs the whole base)

| region | bytes | raw literal | normalized payload literal | refs stream cost | explained by references |
|---|--:|--:|--:|--:|--:|
| shdr | 1664 | 119 | 119 | 0 | 0 |
| gap | 1896 | 16 | 16 | 0 | 0 |
| .text | 2043793 | 31409 | 97 | 274 | 31038 |
| .rodata | 125953 | 4871 | 2 | 10 | 4859 |
| .gopclntab | 1576316 | 41663 | 41198 | 48 | 417 |
| .go.type | 368240 | 108581 | 284 | 158 | 108139 |
| .go.func | 3208 | 65 | 8 | 10 | 47 |
| gap | 2936 | 16 | 16 | 0 | 0 |
| .go.module | 568 | 212 | 61 | 86 | 65 |
| .noptrdata | 58305 | 176 | 0 | 10 | 166 |
| .data | 33906 | 3659 | 0 | 96 | 3563 |
| gap | 846 | 6 | 6 | 0 | 0 |
| .debug_line | 410536 | 410321 | 410321 | 0 | 0 |
| .debug_frame | 77520 | 1872 | 1872 | 0 | 0 |
| .debug_info | 688464 | 688464 | 688464 | 0 | 0 |
| .debug_loclists | 389165 | 349433 | 349433 | 0 | 0 |
| .debug_rnglists | 176945 | 172614 | 172614 | 0 | 0 |
| .debug_addr | 12064 | 12019 | 12019 | 0 | 0 |
| .symtab | 136848 | 29463 | 28930 | 42 | 491 |
| .strtab | 174630 | 3 | 3 | 0 | 0 |
| **total** | 6285736 | 1854982 | 1705463 | 734 | 148785 |

##### attribution, add function r1→r2, strip (literal bytes vs the whole base)

| region | bytes | raw literal | normalized payload literal | refs stream cost | explained by references |
|---|--:|--:|--:|--:|--:|
| shdr | 1024 | 39 | 39 | 0 | 0 |
| gap | 2536 | 16 | 16 | 0 | 0 |
| .text | 2043793 | 31409 | 97 | 129 | 31183 |
| .rodata | 125953 | 4871 | 2 | 10 | 4859 |
| .gopclntab | 1576316 | 41663 | 41198 | 40 | 425 |
| .go.type | 368240 | 108581 | 284 | 32 | 108265 |
| .go.func | 3208 | 65 | 0 | 10 | 55 |
| gap | 2936 | 16 | 16 | 0 | 0 |
| .go.module | 568 | 212 | 61 | 73 | 78 |
| .noptrdata | 58305 | 176 | 0 | 10 | 166 |
| .data | 33906 | 3659 | 0 | 10 | 3649 |
| gap | 846 | 6 | 6 | 0 | 0 |
| **total** | 4219040 | 190713 | 41719 | 314 | 148680 |

Summary (add function):

| class | std (unstripped) | strip |
|---|--:|--:|
| reference / displacement fields (`.text`, `.rodata`, `.go.type`, `.data`, `.noptrdata`, `.go.func`, `.go.module`) | 148.8 kB (8.0% of changed bytes) | 148.7 kB (78%) |
| compressed DWARF (`.debug_*`) | 1 635 kB (88%) | — |
| Go function-table internals (offsets into pctab/funcname/cu tables: table-internal positions, not addresses) | 41 kB (2.2%) | 41 kB (22%) |
| `.symtab` (`st_name` string-table offsets shift after the first inserted symbol; `st_value` is masked) | 29 kB (1.6%) | — |
| section headers, notes, gaps | 0.2 kB | 0.1 kB |
| real code/data change (masked `.text`, `.rodata`, …) | 0.4 kB | 0.3 kB |
| unclassified | 0 | 0 |

About 99.8% of the non-debug difference of a 56-byte source change is address, offset or reference churn.

## H. Pairwise delta (question 7, storage versus CDC)

The `Δ` columns above. Stripped add-function: raw delta 3.4% → normalized delta **0.3%** (payload 16 kB + references 2.9 kB); const+string
3.6 → 1.3; data table 2.2 → 0.2; new packages 9.8 → 3.4. XOR control (equal-size pairs):

| pair | flags | xor+DEFLATE % | Δ raw % |
|---|---|--:|--:|
| const+string r0→r1 | strip | 31.4 | 3.6 |
| add function r1→r2 | strip | 28.6 | 3.4 |
| change body r2→r3 | strip | 8.8 | 0.1 |
| remove function r2→r1 | strip | 28.6 | 3.4 |
| reorder r2→swap | strip | 0.5 | 0.1 |
| data table r2→data | strip | 3.5 | 2.2 |

XOR + DEFLATE (28.6% on the add-function pair) is useless once layout moves; the delta primitive already finds the moved bytes, and
normalization removes what remains.

## I. Stripping (question 6)

* Go `-s -w` removes `.symtab`, `.strtab` and DWARF but **not** the function table: names and function starts survive in
  `.gopclntab`. That is why stripped Go is the *easier* case here: no compressed DWARF (27% of the file, see J), and units are available.
* With no names at all (`call` units: CALL targets only) the stripped add-function Δ is the same (0.4 vs 0.3); under the early insert 5.6 vs 4.6.
  The approach does not depend on symbol names; it depends on having *some* unit boundaries, and CALL targets are enough.
* The C family has no function table. gcc/clang stripped, call-derived units: see section L (gcc 9.3 → 4.4, clang 3.0 → 1.9 Δ).
  Stripped native binaries are harder (no names; a coarser, call-derived unit set); worth saying plainly.

## J. Build-id and debug sections (question: volatile sections)

##### build metadata: r2 → r3 under different flags (raw CDC %, section+CDC %, raw Δ %)

| flags | file | raw CDC | sect+CDC | raw Δ | full-norm Δ | regions that differ most (literal bytes) |
|---|--:|--:|--:|--:|--:|---|
| std | 6285240 | 45.1 | 37.2 | 26.0 | 26.0 | .debug_info 686281, .debug_line 410054, .debug_loclists 350997 |
| notrim | 6333986 | 76.5 | 74.6 | 27.4 | 25.7 | .debug_info 681347, .debug_line 413814, .debug_loclists 350870 |
| nobid | 6285204 | 45.1 | 37.2 | 26.0 | 26.0 | .debug_info 686281, .debug_line 410054, .debug_loclists 350997 |
| strip | 4219040 | 21.4 | 12.2 | 0.1 | 0.1 | .gopclntab 3754, .note.go.buildid 100, .go.module 74 |

Build ids are noise (`.note.go.buildid` 100 B, `.note.gnu.build-id` 36 B). `-trimpath` matters for CDC (paths in DWARF and pclntab:
45.1 → 76.5 raw CDC) but not for delta. The volatile section is compressed DWARF: 88% of the changed bytes of an unstripped
build, incompressible to CDC because zlib output has no common substrings. Measured:

| section | stored | inflated | stored literal vs base | inflated literal vs base inflated | zlib level that re-creates the bytes |
|---|--:|--:|--:|--:|---|
| .debug_abbrev | 368 | 614 | 0 | 0 | 1 |
| .debug_line | 410536 | 707106 | 410321 | 401 | 1 |
| .debug_frame | 77520 | 223284 | 1872 | 370 | 1 |
| .debug_info | 688464 | 1512000 | 688464 | 931113 | 1 |
| .debug_loclists | 389165 | 895920 | 349433 | 41 | 1 |
| .debug_rnglists | 176945 | 288829 | 172614 | 43 | 1 |
| .debug_addr | 12064 | 33888 | 12019 | 888 | 1 |
| **total** | 1755062 | 3661641 | 1634723 | 932856 | |

Go writes `SHF_COMPRESSED` sections with zlib level 1 and `compress/zlib` level 1 **re-creates every one byte-for-byte**, so the
inflated form can be stored and the compressed bytes regenerated exactly without a Preflate-style oracle. Inflated, `.debug_line`,
`.debug_loclists`, `.debug_rnglists`, `.debug_frame` and `.debug_addr` shrink from 0.95 MB of new bytes to 1.7 kB; `.debug_info`
does not shrink (931 kB of its 1.5 MB inflated form is new) because it carries section offsets (`DW_FORM_ref_addr`, type offsets) that shift with any inserted DIE. That is the same
reference problem in another section and the obvious next workload; it is **not** implemented here (the debug sections are left as
independent opaque regions).

## K. Representation cost (question 8) and performance (question 9)

##### representation size of one build (r2)

| flags | file | payload | meta (refs+units+regions) | meta DEFLATEd | refs | units | meta % of file | objects (CDC) |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| std | 6285736 | 6285736 | 602296 | 275017 | 85399 | 5671 | 9.6% | 124 |
| strip | 4219040 | 4219040 | 528952 | 226904 | 79468 | 4181 | 12.5% | 84 |

Reference records average 6.7-7.1 B (4 B unit id + offset + kind + delta): 9.6-12.5% of the file before compression (4.5-5.4% DEFLATEd).
That is the price of a *first* copy; an unrelated binary pays it with no reuse (see Z3-13K admission). Unit table: 4-6 k entries,
one 4-byte id and a VA delta each (about 5 B/entry).

##### performance, r2 std (best of 3)

| step | MiB/s |
|---|--:|
| parse (ELF + pclntab) | 5668 |
| x86 + pointer scan | 456 |
| normalize (given slots, units) | 184 |
| reconstruct | 1418 |
| SHA-256 verify (reference) | 1394 |

normalize allocates 51.9 MiB for a 6.0 MiB image (51 streams)

Lab-quality numbers (single thread, Go, no tuning): the scan is 405 MiB/s, normalization 145 MiB/s and allocates 8.6× the image
(per-slot structs and a masked copy), reconstruction 1.4 GiB/s (SHA-256 verification alone is 1.5 GiB/s). Nothing here is CPU-prohibitive
for a background representation; normalization would need a tighter implementation before it is on a write path.

## L. Beyond Go, and other architectures

C (gcc 13.3 / clang 18.1, `-O2 -static`, 800 kB, `c0 → c1` = one function inserted first in the object so all libc code moves),
pairwise delta (CDC is meaningless at ten chunks):

| build | raw Δ | reloc+struct Δ | x86 refs Δ | full Δ |
|---|--:|--:|--:|--:|
| gcc, unstripped | 10.2 | 9.1 | 4.8 | **3.3** |
| gcc, stripped (no names; call-derived units) | 9.3 | 9.3 | 4.7 | 4.4 |
| clang, unstripped | 3.8 | 3.4 | 1.5 | **1.1** |
| clang, stripped | 3.0 | 3.0 | 1.9 | 1.9 |

The mechanism is not Go-specific.

`linux/arm64` (no ARM branch/ADRP decoder was written; masks only pointers, symbol values and Go function-table offsets):

##### linux/arm64 std: new bytes as % of the current build

| pair | raw CDC | fixed 4K | section | sect+CDC | func ranges | reloc+struct | x86 refs | full | ORACLE | CDC-4K raw | CDC-4K full | Δ raw | Δ full |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| const+string r0→r1 | 85.3 | 78.2 | 93.8 | 81.9 | 66.2 | 81.6 | 82.0 | 76.2 | 71.7 | 59.9 | 54.9 | 24.3 | 24.3 |
| add function r1→r2 | 90.9 | 88.2 | 99.3 | 88.3 | 76.3 | 87.0 | 88.3 | 86.3 | 83.6 | 64.6 | 58.0 | 28.1 | 26.7 |
| change body r2→r3 | 56.6 | 44.7 | 83.9 | 39.8 | 38.6 | 40.1 | 39.8 | 37.9 | 36.0 | 24.9 | 23.8 | 21.4 | 21.4 |
| remove function r2→r1 | 90.9 | 88.2 | 99.3 | 88.3 | 76.3 | 86.9 | 88.3 | 86.3 | 83.6 | 64.6 | 58.0 | 28.1 | 26.7 |
| early insert r2→early | 94.8 | 95.7 | 99.3 | 94.2 | 89.9 | 96.3 | 94.2 | 98.6 | 94.1 | 69.6 | 70.8 | 27.2 | 25.8 |
| reorder r2→swap | 48.9 | 32.9 | 88.0 | 41.5 | 40.4 | 39.7 | 41.5 | 40.0 | 37.6 | 25.7 | 25.2 | 23.5 | 23.5 |
| data table r2→data | 81.5 | 82.6 | 93.8 | 78.9 | 72.9 | 79.0 | 78.9 | 78.5 | 74.1 | 61.3 | 59.6 | 23.7 | 22.7 |
| new packages r2→deps | 92.8 | 92.1 | 99.3 | 93.3 | 84.8 | 94.4 | 93.4 | 97.7 | 93.3 | 76.4 | 78.1 | 31.7 | 30.8 |

##### linux/arm64 strip: new bytes as % of the current build

| pair | raw CDC | fixed 4K | section | sect+CDC | reloc+struct | x86 refs | full | ORACLE | CDC-4K raw | CDC-4K full | Δ raw | Δ full |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| const+string r0→r1 | 82.5 | 67.7 | 95.1 | 77.6 | 76.1 | 77.7 | 68.0 | 63.1 | 52.7 | 46.2 | 1.3 | 1.4 |
| add function r1→r2 | 88.3 | 82.6 | 99.0 | 85.4 | 82.3 | 85.4 | 81.4 | 78.5 | 54.0 | 45.2 | 3.6 | 1.7 |
| change body r2→r3 | 41.1 | 18.1 | 82.3 | 19.1 | 17.5 | 19.1 | 14.1 | 12.2 | 2.8 | 1.8 | 0.1 | 0.1 |
| remove function r2→r1 | 88.3 | 82.6 | 99.0 | 85.4 | 82.3 | 85.4 | 81.4 | 78.5 | 54.0 | 45.2 | 3.6 | 1.7 |
| early insert r2→early | 94.6 | 93.6 | 99.0 | 93.6 | 95.6 | 93.6 | 98.9 | 93.5 | 69.5 | 70.7 | 10.1 | 8.3 |
| data table r2→data | 82.5 | 74.3 | 99.9 | 80.6 | 79.6 | 80.6 | 78.9 | 73.4 | 58.6 | 56.2 | 4.7 | 3.3 |

Δ add function stripped 3.6 → 1.7 from pointers and function-table offsets alone; the BL / ADRP+ADD references that dominate x86's gain here
are the unimplemented part.

## Answers

1. **Plain section segmentation:** none. `sect+CDC` ≈ raw CDC (within 1-2 points); function ranges −12 points but +13 in entries.
2. **Explicit relocations:** none in static Go; 17.5 k RELATIVE in PIE, worth 11-13 CDC points on append-style changes and nothing under movement.
3. **Encoded x86 references:** the dominant class. Stripped add: 77.4 → 36.9 (CDC), 3.4% → 0.3% (Δ); masked `.text` differs in 97 bytes instead of 31 409.
4. **Stable identity:** symbol name where it exists; region-relative close behind for append-style changes; content for stripped code but needs a collision policy; ordinal fails under insertion.
5. **Insertion/reordering:** reorder inside a package is nearly free; insertion before everything is the hard case (Δ 11.9 → 4.6 stripped).
6. **Stripped:** easier for Go (no DWARF, names survive in pclntab); no dependence on names (call-derived units are enough); native stripped binaries gain less (gcc 9.3 → 4.4).
7. **Incremental cost vs CDC:** production CDC 74.2 → 34.9 (stripped add), 81.1 → 55.8 (std add); CDC-4K 55.7 → 6.5; pairwise delta 3.4 → 0.3. The gain is real but **production 64 KiB CDC realises only part of it**.
8. **Metadata:** 9.6-12.5% of file, 4.5-5.4% DEFLATEd, 6.7-7.1 B per reference.
9. **Performance:** scan 405, normalize 145, reconstruct 1 444 MiB/s; parse is negligible.
10. **Substrate worth carrying to PE:** yes — `Region`, `Slot`, units, one `Normalize`/`Reconstruct`; the x86 scan and `gopcln` are format-independent already.

## Gate

* reproducible ELF families (Go amd64/arm64, gcc/clang C): yes; bounded parser with deterministic malformed tests and one smoke fuzz: yes;
* structural baseline, relocation normalization (PIE), oracle, instruction references, identity comparison, stripped, build-id/debug: measured;
* every accepted exact row rebuilt byte-identically (length, SHA-256, bytes): yes;
* real Go ELF evidence: yes (this module's own `z3lab`); no production persistence changed.

## Limits

* One program family; the Go runtime and standard library dominate the binary. C family is small (800 kB).
* Slot detection is a byte-pattern scan: unrecognised forms (a few AVX-512-specific sequences) stay raw; ARM and RISC-V are unimplemented.
* `st_name` / DWARF offsets / Go function-table-internal offsets (positions inside the same table) are unnormalized "table-offset" references.
* Data-target identity without data symbols is region-relative only.
* Production-CDC numbers are an accounting of new chunk bytes (+96 B/reference reported separately in `pair`), not a store; Δ is a measuring instrument, not a codec.

## Tests actually run

`go test ./binref ./elfz3 ./gopcln ./cmd/z3bin` (substrate round trips under every identity, shift invariance, corrupt-stream no-panic, x86 fixtures; ELF good case,
25 malformed/overflow/index cases, overlapping sections; synthetic Go pclntab; delta and scaled-CDC sanity). One 25 s smoke fuzz of `elfz3.FuzzParse`
(whatever `Parse` accepts must normalize and reconstruct byte-for-byte): it found one genuine defect in the first 3 s (a NULL section named as the section-name table sliced
out of bounds); fixed, preserved as a deterministic case and as `testdata/fuzz/FuzzParse/…`, and the rerun made 3.5 M executions clean.
No DEFLATE/tar/Safetensors/tensor fuzzing and no root ZeroS3 tests were run.
