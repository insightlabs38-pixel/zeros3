# Z3-13 — PE/COFF generalization

Laboratory result; no production code, root module or persistent format touched. Z3-12's substrate (`binref`: regions, reference
slots, units, one `Normalize`/`Reconstruct`, the x86 scan; `gopcln`) is used **unchanged in kind**; the only new code that is about PE is
`pez3/` (the container parser). Reproduce: `z3bin corpus -out DIR -goos windows -goroot COPY_OF_GOROOT`, `z3bin report -corp DIR -goos windows -s
matrix|ident|attr|meta|perf|classes|admit`, `-s xformat` (ELF vs PE), `z3bin verify -corp DIR` (exact reconstruction of every file).

## A. Corpus

The same source revisions as Z3-12 (`r0 … deps`, one overlay file in package `main`; `early` adds a function to package `errors`), built with
`GOOS=windows GOARCH=amd64 CGO_ENABLED=0` by the same go1.27.0 toolchain, flag sets `std` (`-trimpath`) and `strip` (`-ldflags=-s -w`), plus `notrim` and
`nobid` for r0/r2/r3, plus an unrelated program (`cmd/z3tar`) as the admission control. Sizes: std 6.38 MB (2.07 MB `.text`, 2.09 MB `.rdata`, 1.7 MB
`.zdebug_*`, 0.28 MB COFF symbols), strip 4.35 MB. Builds are reproducible (SHA-256 recorded in `windows-amd64-provenance.json`).
A Go PE has 8 (strip) / 16 (std) sections, 18 384 base-relocation entries (18 333 `DIR64`, the rest page padding), 1 import descriptor (99 import slots), no exports,
3 653 `RUNTIME_FUNCTION` entries (`.pdata`), and the same Go function table as ELF, found by the same code. No MinGW/MSVC binaries or signed samples were
available locally; none were fabricated.

## B. Parser (bounded) and what stays opaque

`pez3.Parse` handles PE32+ only (PE32 is `ErrUnsupported`). It validates: DOS/PE signatures and `e_lfanew`, section count (≤ 256), optional-header size and
directory count (≤ 16), every section's raw range (offset+size overflow and file bounds), `/nnn` long names against the COFF string table, symbol/string table
extents, auxiliary-symbol counts, base-relocation block sizes (≥ 8, even, inside the directory), import/export/exception/debug directories (RVA mapped to file
bytes, sizes inside the section, entry counts bounded), and a global budget on import-thunk entries (descriptors may share one thunk array). It never
interprets: the DOS stub, the certificate table (data directory 4 is a **file offset**, not an RVA, and lies after the last section), COFF line numbers,
resources, TLS, load config, delay imports, CLR headers. Those bytes are in ordinary regions and are reconstructed byte for byte. Slots come from:
base relocations (`DIR64`, plus each block's page RVA), import descriptors (three RVAs) and thunk arrays, export directory (4 RVAs, function and name tables),
`.pdata` (3 RVAs per entry), debug directory (`AddressOfRawData`), `AddressOfEntryPoint`. RVA, VA and file offset stay distinct: slot positions are file offsets;
targets are VAs (`ImageBase+RVA`); `EncRVA32` subtracts the image base, `EncAbs64` is a VA, `EncRel32` is relative to the slot's own VA. Output bytes are never canonicalized.

**Signing boundary.** The certificate table is carried as opaque bytes (unit test: a certificate filled with address-like words is reproduced exactly and
none of it is normalized). Because reconstruction is byte-exact, an Authenticode signature present in an input would be byte-identical afterwards; no
signature validation was performed or claimed, and no signed sample was tested.

## C. Structural baseline and the same mutations on PE (questions 1, 8)

Percent of the current build that is new bytes; columns as in Z3-12 (`ORACLE` = payload only, no metadata counted — a bound, not a cost; `CDC-4K` and `Δ`
are sensitivity/pairwise-delta instruments).

#### windows/amd64 std: new bytes as % of the current build

| pair | raw CDC | fixed 4K | section | sect+CDC | func ranges | reloc+struct | x86 refs | full | ORACLE | CDC-4K raw | CDC-4K full | Δ raw | Δ full |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| const+string r0→r1 | 81.0 | 78.1 | 99.8 | 81.2 | 72.4 | 81.0 | 57.8 | 57.4 | 47.6 | 66.2 | 41.9 | 27.5 | 26.4 |
| add function r1→r2 | 83.3 | 90.2 | 99.9 | 82.4 | 70.7 | 82.7 | 55.8 | 55.8 | 49.3 | 64.2 | 36.3 | 27.3 | 25.9 |
| change body r2→r3 | 34.5 | 33.3 | 92.5 | 32.1 | 30.5 | 32.1 | 32.6 | 32.6 | 32.6 | 27.2 | 27.3 | 25.7 | 25.7 |
| remove function r2→r1 | 83.3 | 90.2 | 99.9 | 82.4 | 70.7 | 82.7 | 55.8 | 55.8 | 49.3 | 64.2 | 36.3 | 27.3 | 25.9 |
| early insert r2→early | 90.6 | 98.2 | 99.9 | 91.5 | 87.7 | 93.7 | 71.3 | 73.1 | 63.6 | 72.6 | 48.1 | 30.2 | 26.0 |
| reorder r2→swap | 32.8 | 26.9 | 97.8 | 30.7 | 29.1 | 32.3 | 34.0 | 35.3 | 30.7 | 17.9 | 18.3 | 15.6 | 15.6 |
| data table r2→data | 77.8 | 81.3 | 93.8 | 75.2 | 65.9 | 75.5 | 55.3 | 55.3 | 46.4 | 57.8 | 28.4 | 19.5 | 17.7 |
| new packages r2→deps | 91.9 | 99.7 | 100.0 | 91.9 | 87.9 | 92.7 | 74.2 | 74.7 | 64.5 | 78.4 | 54.4 | 33.6 | 29.7 |

#### windows/amd64 strip: new bytes as % of the current build

| pair | raw CDC | fixed 4K | section | sect+CDC | reloc+struct | x86 refs | full | ORACLE | CDC-4K raw | CDC-4K full | Δ raw | Δ full |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| const+string r0→r1 | 76.0 | 76.0 | 99.9 | 76.5 | 76.6 | 40.5 | 40.6 | 27.2 | 58.7 | 17.1 | 3.7 | 2.5 |
| add function r1→r2 | 77.5 | 87.4 | 99.9 | 76.2 | 77.6 | 40.7 | 41.6 | 27.7 | 55.6 | 18.6 | 3.5 | 2.2 |
| change body r2→r3 | 8.5 | 1.3 | 95.7 | 7.2 | 7.2 | 7.9 | 7.9 | 7.9 | 1.6 | 2.1 | 0.1 | 0.1 |
| remove function r2→r1 | 77.5 | 87.4 | 99.9 | 76.2 | 77.6 | 40.7 | 41.6 | 27.7 | 55.5 | 18.6 | 3.5 | 2.2 |
| early insert r2→early | 89.2 | 97.8 | 99.9 | 89.7 | 94.8 | 68.2 | 72.9 | 59.2 | 70.6 | 35.2 | 11.3 | 5.9 |
| reorder r2→swap | 17.8 | 1.8 | 96.7 | 15.5 | 17.8 | 17.3 | 19.1 | 15.5 | 2.2 | 3.5 | 0.1 | 0.1 |
| data table r2→data | 76.0 | 72.5 | 100.0 | 74.7 | 76.0 | 45.5 | 46.4 | 32.5 | 57.3 | 21.9 | 2.9 | 1.5 |
| new packages r2→deps | 90.2 | 99.6 | 100.0 | 90.2 | 92.2 | 63.0 | 64.6 | 50.0 | 73.3 | 38.1 | 9.8 | 4.9 |

Section segmentation alone does not help on PE either: `sect+CDC` ≈ raw CDC (76.2 vs 77.5 stripped add-function), whole sections are 99.9% new.

## D. Base relocations (question 2)

Which classes pay (identity: symbol/unit, `units=auto`; PE strip, add function; the ELF table is the contrast). `relocations only` = the PE base-relocation table;
`format tables` = imports, exports, `.pdata`, debug, entry point, relocation page RVAs (ELF: `st_value` slots; none in a stripped file).

##### windows/amd64 strip, add function r1→r2

| reference classes masked | CDC % | CDC-4K % | Δ % | reference metadata B | references |
|---|--:|--:|--:|--:|--:|
| none (raw) | 77.5 | 55.6 | 3.5 | 0 | 0 |
| relocations only | 75.9 | 51.3 | 3.0 | 116965 | 18328 |
| format tables (no Go) | 76.6 | 55.5 | 3.5 | 90189 | 11081 |
| relocations + format tables | 76.2 | 51.4 | 3.0 | 203211 | 29409 |
| Go function table only | 78.1 | 55.8 | 3.2 | 82288 | 8325 |
| x86 branch + RIP only | 40.7 | 22.4 | 3.2 | 391114 | 56625 |
| x86 + relocations | 40.3 | 18.4 | 2.6 | 507147 | 74953 |
| x86 + relocations + tables + Go table | 41.6 | 18.6 | 2.2 | 636006 | 94359 |
| all (+ pointer scan) | 41.6 | 18.6 | 2.2 | 636006 | 94359 |

##### windows/amd64 strip, early insert r2→early

| reference classes masked | CDC % | CDC-4K % | Δ % | reference metadata B | references |
|---|--:|--:|--:|--:|--:|
| none (raw) | 89.2 | 70.6 | 11.3 | 0 | 0 |
| relocations only | 92.4 | 70.8 | 10.4 | 116981 | 18330 |
| format tables (no Go) | 90.8 | 71.0 | 11.1 | 90210 | 11084 |
| relocations + format tables | 93.4 | 71.0 | 10.2 | 203243 | 29414 |
| Go function table only | 91.6 | 71.5 | 10.7 | 82326 | 8329 |
| x86 branch + RIP only | 68.2 | 34.6 | 7.5 | 391173 | 56632 |
| x86 + relocations | 70.8 | 34.7 | 6.6 | 507215 | 74962 |
| x86 + relocations + tables + Go table | 72.9 | 35.2 | 5.9 | 636118 | 94375 |
| all (+ pointer scan) | 72.9 | 35.2 | 5.9 | 636118 | 94375 |

Contrast, ELF (no relocation table exists; the pointer scan plays its role):

##### linux/amd64 strip, add function r1→r2

| reference classes masked | CDC % | CDC-4K % | Δ % | reference metadata B | references |
|---|--:|--:|--:|--:|--:|
| none (raw) | 74.2 | 55.7 | 3.4 | 0 | 0 |
| relocations only | 75.4 | 55.3 | 3.4 | 147 | 0 |
| format tables (no Go) | 75.4 | 55.3 | 3.4 | 147 | 0 |
| relocations + format tables | 75.4 | 55.3 | 3.4 | 147 | 0 |
| Go function table only | 77.4 | 55.7 | 3.0 | 82677 | 8363 |
| x86 branch + RIP only | 36.9 | 12.7 | 2.0 | 358775 | 53375 |
| x86 + relocations | 36.9 | 12.7 | 2.0 | 358775 | 53375 |
| x86 + relocations + tables + Go table | 38.4 | 13.0 | 1.6 | 422991 | 61738 |
| all (+ pointer scan) | 34.9 | 6.5 | 0.3 | 528952 | 79468 |

* **Base relocations alone are worth 1.6 CDC points and 4.3 CDC-4K points** (77.5 → 75.9, 55.6 → 51.3; Δ 3.5 → 3.0) on the add-function pair. They list the
  data pointers (type descriptors, itabs, tables); they say nothing about the 56 625 branch / RIP displacements in the text, which is where the production-CDC gain comes from (77.5 → 40.7).
  Under movement (`early`) they are neutral-to-harmful at production CDC (89.2 → 92.4) for the same reason that they were in PIE ELF.
* They are redundant with the pointer scan: in PE `x86 + relocations + tables + Go table` equals `all (+ pointer scan)` to the decimal, because every aligned
  pointer the scan finds is already listed (18 463 duplicate slots dropped as overlaps); in static ELF, where no relocations exist, the **pointer scan supplies the same class**
  (CDC-4K 12.7 → 6.5 on the stripped add-function pair, ELF table in Z3-12 §D/§E).
* The reference classes the format *lists* (imports, exports, `.pdata`, entry point, page RVAs) move the numbers by at most 0.9 CDC points and 0.0 Δ points: the PE import table of a Go program is one
  descriptor, there are no exports, and `.pdata` explains 1.1 kB of the 211 kB that changes (§E).

## E. Where the changed bytes are on PE (add function, stripped; literal bytes against the whole base)

##### attribution, add function r1→r2, strip (literal bytes vs the whole base)

| region | bytes | raw literal | normalized payload literal | refs stream cost | explained by references |
|---|--:|--:|--:|--:|--:|
| pe-headers | 1408 | 46 | 52 | 8 | 0 |
| .text | 2073088 | 31362 | 201 | 45208 | 0 |
| .rdata | 2093056 | 155844 | 41370 | 25548 | 88926 |
| .data | 100864 | 3846 | 106 | 1092 | 2648 |
| .pdata | 44032 | 1109 | 0 | 28 | 1081 |
| .idata | 1536 | 0 | 0 | 10 | 0 |
| .reloc | 37888 | 19160 | 19073 | 307 | 0 |
| .symtab | 512 | 0 | 16 | 0 | 0 |
| **total** | 4353024 | 211367 | 60818 | 72201 | 78348 |

* Instruction-level references again explain nearly everything in code: `.text` 31 362 → 201 literal bytes; `.pdata` 1 109 → 0 (RUNTIME_FUNCTION RVAs, normalized by the format-listed slots);
  `.data` 3 846 → 106.
* `.rdata` (Go's rodata + function table + type descriptors share one PE section) keeps 41 kB of function-table-internal offsets (same 41 kB as ELF) and costs 25.5 kB of
  reference records; **`.text` references cost 45 kB** versus 0.1 kB on ELF. The difference is the identity of *data* targets: with no data symbols the target is
  "section + offset", ELF's Go linker emits separate `.rodata`, `.go.type`, `.gopclntab`, `.go.func`, so an insertion moves one section's offsets; the PE linker emits one `.rdata`,
  so the same insertion moves most of the references that point into it. (Attribution supports it; the ablation of splitting `.rdata` was not run.) This is the main reason stripped PE
  reaches Δ 2.2% where ELF reaches 0.3%.
* `.reloc` itself changes by 19 kB (page RVAs and in-page offsets): it is a derived list of where the pointers are, the same information the pointer slots carry, and is stored as ordinary bytes.
  Predicting it from the slots is an obvious follow-up (about 0.4% of the file); it was not done.
* There is no `.symtab`/DWARF cost in the stripped file. Std PE carries Go's compressed DWARF (`.zdebug_*`, zlib-compressed with a `ZLIB` header): 87% of changed bytes there, as in ELF, and
  left as opaque regions (Z3-12 §J shows the ELF equivalent is exactly regenerable).

## F. One reference decoder (question: no second mechanism)

There is no PE branch decoder. `binref.ScanX86` is called with an `Image` whose executable regions were produced by `pez3` instead of `elfz3`; the call is the same function, the same
patterns, the same acceptance rules. The same goes for `ScanPtr`, `gopcln.Find` (it recognises the function table in `.rdata` by its header; the text start is the first executable region),
`CleanUnits`, `Normalize`, `Reconstruct`. The numbers above are the answer to "does it carry over": `x86 branch + RIP only` takes the stripped add-function pair from 77.5% to 40.7% (CDC) and
3.5% to 3.2% (Δ) on PE, from 74.2% to 36.9% and 3.4% to 2.0% on ELF.

## G. RVA / VA / file offset (question: explicit distinction)

* Slot positions are **file offsets** (the only thing needed to patch bytes). `rvaOff` maps an RVA through the section table to a file offset *and* the bytes available from it; nothing assumes
  RVA = offset or section alignment = file alignment.
* Targets are **VAs**; `EncAbs64` stores a VA, `EncRVA32` stores `VA − ImageBase` (the image base is in the regions stream header), `EncRel32` stores `target − (slot VA + 4 + bias)`; the Go
  function table's 32-bit entries are `VA − text start` (`EncOff32`, second base).
* Normalized form names the target as *(unit, offset)* or *(region, offset)*. Section RVAs shift whenever an earlier section crosses a page; offsets within a region do not, which is why
  region-relative identity survives `.text` growing across a page boundary (the stripped add-function PE case does this).
* Nothing is canonicalized; `ImageBase`, section alignment and the DOS/PE headers are reproduced as found.

## H. Same mutation, ELF versus PE (the key cross-format evidence)

New bytes as % of the current build, raw → with all reference classes masked (`full`), production CDC and pairwise delta.

##### same source mutation, ELF versus PE, strip (new bytes as % of the current build)

| mutation | ELF CDC raw → full | ELF Δ raw → full | PE CDC raw → full | PE Δ raw → full |
|---|--:|--:|--:|--:|
| const+string r0→r1 | 69.5 → 31.6 | 3.6 → 1.3 | 76.0 → 40.6 | 3.7 → 2.5 |
| add function r1→r2 | 74.2 → 34.9 | 3.4 → 0.3 | 77.5 → 41.6 | 3.5 → 2.2 |
| change body r2→r3 | 21.4 → 16.1 | 0.1 → 0.1 | 8.5 → 7.9 | 0.1 → 0.1 |
| remove function r2→r1 | 74.2 → 34.9 | 3.4 → 0.3 | 77.5 → 41.6 | 3.5 → 2.2 |
| early insert r2→early | 88.4 → 69.1 | 11.9 → 4.6 | 89.2 → 72.9 | 11.3 → 5.9 |
| reorder r2→swap | 18.2 → 19.6 | 0.1 → 0.1 | 17.8 → 19.1 | 0.1 → 0.1 |
| data table r2→data | 72.5 → 41.0 | 2.2 → 0.2 | 76.0 → 46.4 | 2.9 → 1.5 |
| new packages r2→deps | 87.9 → 65.7 | 9.8 → 3.4 | 90.2 → 64.6 | 9.8 → 4.9 |

##### same source mutation, ELF versus PE, std (new bytes as % of the current build)

| mutation | ELF CDC raw → full | ELF Δ raw → full | PE CDC raw → full | PE Δ raw → full |
|---|--:|--:|--:|--:|
| const+string r0→r1 | 77.1 → 51.3 | 28.2 → 26.5 | 81.0 → 57.4 | 27.5 → 26.4 |
| add function r1→r2 | 81.1 → 55.8 | 28.5 → 26.4 | 83.3 → 55.8 | 27.3 → 25.9 |
| change body r2→r3 | 45.1 → 39.9 | 26.0 → 26.0 | 34.5 → 32.6 | 25.7 → 25.7 |
| remove function r2→r1 | 81.1 → 55.8 | 28.4 → 26.4 | 83.3 → 55.8 | 27.3 → 25.9 |
| early insert r2→early | 88.7 → 73.0 | 31.2 → 25.9 | 90.6 → 73.1 | 30.2 → 26.0 |
| reorder r2→swap | 43.9 → 42.9 | 23.9 → 23.9 | 32.8 → 35.3 | 15.6 → 15.6 |
| data table r2→data | 74.9 → 52.2 | 20.5 → 19.1 | 77.8 → 55.3 | 19.5 → 17.7 |
| new packages r2→deps | 90.7 → 77.7 | 34.0 → 29.6 | 91.9 → 74.7 | 33.6 → 29.7 |

Stripped add-function: **ELF 74.2 → 34.9 (Δ 3.4 → 0.3); PE 77.5 → 41.6 (Δ 3.5 → 2.2)**. The ordering of mutations, the size of the effect on code-reference churn, and the failure on
`reorder` (no gain at production CDC because the layout barely moves) are the same on both formats; PE is behind by about 7 CDC points and 1.9 Δ points, almost all explained by the single-`.rdata`
identity effect and the `.reloc` table (§E). A third container (Mach-O, §K) behaves the same way.

## I. Identities on PE

##### target identities, std (CDC % / pairwise-delta %)

| pair | units | ordinal | region-rel | symbol | content |
|---|---|--:|--:|--:|--:|
| add function r1→r2 | auto | 59.5 / 27.7 | 57.2 / 26.7 | 55.8 / 25.9 | 58.1 / 26.7 |
| early insert r2→early | auto | 73.8 / 27.9 | 71.5 / 27.5 | 73.1 / 26.0 | 99.6 / 28.0 |
| reorder r2→swap | auto | 32.4 / 15.6 | 34.7 / 15.6 | 35.3 / 15.6 | 33.9 / 15.7 |

##### target identities, strip (CDC % / pairwise-delta %)

| pair | units | ordinal | region-rel | symbol | content |
|---|---|--:|--:|--:|--:|
| add function r1→r2 | pcln | 42.4 / 2.2 | 40.5 / 2.2 | 41.6 / 2.2 | 42.2 / 2.3 |
| add function r1→r2 | call | 42.4 / 2.2 | 40.6 / 2.3 | n/a (no names) | 43.0 / 2.2 |
| early insert r2→early | pcln | 63.4 / 6.8 | 61.6 / 6.8 | 72.9 / 5.9 | 92.5 / 6.6 |
| early insert r2→early | call | 94.8 / 6.8 | 93.0 / 6.9 | n/a (no names) | 99.7 / 6.3 |
| reorder r2→swap | pcln | 18.4 / 0.1 | 21.6 / 0.1 | 19.1 / 0.1 | 20.0 / 0.1 |
| reorder r2→swap | call | 19.1 / 0.1 | 21.6 / 0.1 | n/a (no names) | 20.8 / 0.1 |

Same ranking as ELF (symbol ≥ content ≥ region-relative ≈ ordinal under movement; content collapses at production CDC because identical function bodies cannot be told apart). One defect in the
shared substrate surfaced here, not on ELF: Go's COFF symbol table lists aliases at the same address (`runtime.text`, `go:buildid` at offset 0); `CleanUnits` let the first one swallow the whole
section, leaving 6 usable units and un-normalized data. It now keeps one unit per address, preferring the sized one (std PE add-function CDC 62.5 → 55.8). The ELF numbers did not move.

## J. Substrate review — what is actually common

Code size: engine `binref` 918 lines (image/partition 269, normalize/reconstruct 509, x86 140); `elfz3` 288; `pez3` 488; `gopcln` 106 (shared by ELF, PE and the Mach-O view).
Counts of accepted references per encoding on stripped r2 (`ByEnc`):

| encoding | ELF | PE | Mach-O (lab view) | verdict |
|---|--:|--:|--:|---|
| `EncRel32` (+ bias) | 53 375 | 56 625 | 52 050 | shared; one scan |
| `EncAbs64` | 17 730 (ptr scan) | 18 328 (base relocs) | 17 340 (ptr scan) | shared; source differs, mechanism does not |
| `EncOff32` (Go function table) | 8 363 | 8 325 | 8 289 | shared via `gopcln` |
| `EncRVA32` | 0 | 11 081 | 0 | PE-specific value of an otherwise generic "32-bit offset from an image base" (same arm as `EncOff32` with a different base) |
| `EncAbs32` | 0 | 0 | 0 | **deleted**: never produced by either format in any corpus |

Kept (used by both): `Image`, `Region` (incl. zero-length virtual regions with `VSize`: ELF NOBITS, PE zero-fill tails), `Partition` (written for ELF, reused verbatim by PE and the Mach-O view),
`Slot{Off, Enc, Bias, Class}`, `Sym`/`Unit`/`CleanUnits`, `Ident{Symbol, RegionRel, Content}`, `Norm`, `Reconstruct`, `ScanX86`, `ScanPtr`.
Removed during this batch: `EncAbs32` (above) and `Slot.Tag` (a format sub-kind used only to slice the lab's tables; the PE directory split it provided showed that no table directory moves a number, §D).
Kept as a control only, to be dropped when carried forward: `IdentOrdinal` (loses to symbol under movement on both formats).
What PE needed from the engine that ELF had not: nothing new in kind — one `Partition` helper factored out, `CleanUnits` taking one unit per address, one more `Enc` arm. What ELF needed that PE did not: nothing.

## K. Mach-O characterization only

`GOOS=darwin GOARCH=amd64`, same revisions, a ~50-line lab view of `debug/macho` sections as `binref` regions (`cmd/z3bin/macho.go`; no fixups, no signature handling, not a package). Not an adapter.

##### darwin/amd64 strip: new bytes as % of the current build

| pair | raw CDC | fixed 4K | section | sect+CDC | reloc+struct | x86 refs | full | ORACLE | CDC-4K raw | CDC-4K full | Δ raw | Δ full |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| const+string r0→r1 | 77.4 | 66.5 | 96.3 | 73.7 | 82.8 | 37.5 | 39.7 | 28.1 | 53.4 | 14.2 | 1.2 | 1.1 |
| add function r1→r2 | 82.1 | 94.4 | 95.4 | 77.5 | 84.2 | 54.9 | 60.7 | 50.1 | 60.0 | 10.3 | 6.4 | 0.4 |
| change body r2→r3 | 24.8 | 17.7 | 84.0 | 12.7 | 21.2 | 12.7 | 21.2 | 19.8 | 3.4 | 3.4 | 0.1 | 0.1 |
| remove function r2→r1 | 82.0 | 94.4 | 95.4 | 77.5 | 84.1 | 54.8 | 60.7 | 50.0 | 59.9 | 10.2 | 6.4 | 0.4 |
| early insert r2→early | 90.7 | 96.8 | 99.9 | 90.6 | 92.5 | 68.0 | 71.8 | 59.6 | 69.8 | 34.4 | 10.4 | 4.5 |
| reorder r2→swap | 28.8 | 17.8 | 84.0 | 22.1 | 28.7 | 23.3 | 29.5 | 26.8 | 4.0 | 4.3 | 0.1 | 0.1 |
| data table r2→data | 80.4 | 96.3 | 97.6 | 78.0 | 84.7 | 54.3 | 61.8 | 50.0 | 59.2 | 17.6 | 3.6 | 0.3 |
| new packages r2→deps | 91.3 | 94.2 | 99.9 | 91.2 | 93.2 | 66.9 | 70.7 | 58.6 | 71.5 | 38.7 | 6.9 | 3.5 |

The same problem appears and the same mechanism, unchanged, answers it: stripped add-function raw CDC 82.1 → 54.9 (x86 refs), Δ 6.4 → 0.4. Sections/segments are the only format-specific input;
`__gopclntab` is found by the same `gopcln.Find`.

## L. Admission and fallback (question 8; Z3-13K)

Chooser: for each candidate representation, measure new bytes against the previous build plus 96 B per reference (the lab's `chunkRef`); take the cheapest, but a candidate must beat raw CDC by
at least half a point. No global index: one base, one pair.

##### admission by measured cost, windows/amd64 strip (new bytes + 96 B per reference, % of current build)

| pair | raw CDC | section+CDC | x86 refs | full | chosen | saving vs raw (points) |
|---|--:|--:|--:|--:|---|--:|
| const+string r0→r1 | 76.1 | 76.6 | 40.7 | 40.8 | x86 refs | 35.4 |
| add function r1→r2 | 77.6 | 76.3 | 40.8 | 41.8 | x86 refs | 36.8 |
| change body r2→r3 | 8.6 | 7.3 | 8.1 | 8.1 | section + CDC | 1.3 |
| remove function r2→r1 | 77.6 | 76.3 | 40.8 | 41.8 | x86 refs | 36.8 |
| early insert r2→early | 89.3 | 89.8 | 68.3 | 73.0 | x86 refs | 21.0 |
| reorder r2→swap | 17.9 | 15.7 | 17.4 | 19.3 | section + CDC | 2.2 |
| data table r2→data | 76.1 | 74.8 | 45.6 | 46.6 | x86 refs | 30.5 |
| new packages r2→deps | 90.3 | 90.3 | 63.1 | 64.7 | x86 refs | 27.2 |
| unrelated program (r2 → other) | 100.1 | 100.1 | 107.1 | 112.7 | raw CDC | 0.0 |

##### admission by measured cost, windows/amd64 std (new bytes + 96 B per reference, % of current build)

| pair | raw CDC | section+CDC | x86 refs | full | chosen | saving vs raw (points) |
|---|--:|--:|--:|--:|---|--:|
| const+string r0→r1 | 81.1 | 81.3 | 58.0 | 57.5 | x86 refs | 23.2 |
| add function r1→r2 | 83.4 | 82.5 | 56.0 | 56.0 | x86 refs | 27.4 |
| change body r2→r3 | 34.6 | 32.2 | 32.8 | 32.8 | section + CDC | 2.4 |
| remove function r2→r1 | 83.4 | 82.5 | 56.0 | 56.0 | x86 refs | 27.4 |
| early insert r2→early | 90.7 | 91.6 | 71.4 | 73.3 | x86 refs | 19.3 |
| reorder r2→swap | 32.9 | 30.8 | 34.1 | 35.4 | section + CDC | 2.1 |
| data table r2→data | 77.9 | 75.3 | 55.4 | 55.4 | x86 refs | 22.5 |
| new packages r2→deps | 92.1 | 92.1 | 74.4 | 74.8 | x86 refs | 17.7 |
| unrelated program (r2 → other) | 100.1 | 100.1 | 105.3 | 108.5 | raw CDC | 0.0 |

##### admission by measured cost, linux/amd64 strip (new bytes + 96 B per reference, % of current build)

| pair | raw CDC | section+CDC | x86 refs | full | chosen | saving vs raw (points) |
|---|--:|--:|--:|--:|---|--:|
| const+string r0→r1 | 69.6 | 71.2 | 33.7 | 31.8 | full | 37.8 |
| add function r1→r2 | 74.3 | 75.6 | 37.1 | 35.0 | full | 39.3 |
| change body r2→r3 | 21.5 | 12.4 | 12.4 | 16.3 | section + CDC | 9.1 |
| remove function r2→r1 | 74.3 | 75.6 | 37.1 | 35.0 | full | 39.3 |
| early insert r2→early | 88.5 | 88.5 | 64.8 | 69.3 | x86 refs | 23.7 |
| reorder r2→swap | 18.3 | 14.0 | 17.8 | 19.7 | section + CDC | 4.3 |
| data table r2→data | 72.7 | 70.2 | 37.7 | 41.2 | x86 refs | 35.0 |
| new packages r2→deps | 88.0 | 91.0 | 62.0 | 65.9 | x86 refs | 26.0 |
| unrelated program (r2 → other) | 100.1 | 100.1 | 106.5 | 110.5 | raw CDC | 0.0 |

##### admission by measured cost, linux/amd64 std (new bytes + 96 B per reference, % of current build)

| pair | raw CDC | section+CDC | x86 refs | full | chosen | saving vs raw (points) |
|---|--:|--:|--:|--:|---|--:|
| const+string r0→r1 | 77.2 | 76.8 | 52.4 | 51.5 | full | 25.7 |
| add function r1→r2 | 81.3 | 81.4 | 57.0 | 56.0 | full | 25.3 |
| change body r2→r3 | 45.3 | 37.4 | 37.4 | 40.1 | section + CDC | 7.9 |
| remove function r2→r1 | 81.3 | 81.4 | 57.0 | 56.0 | full | 25.3 |
| early insert r2→early | 88.8 | 88.8 | 69.9 | 73.2 | x86 refs | 18.9 |
| reorder r2→swap | 44.0 | 38.6 | 41.1 | 43.1 | section + CDC | 5.4 |
| data table r2→data | 75.0 | 71.1 | 49.8 | 52.4 | x86 refs | 25.2 |
| new packages r2→deps | 90.8 | 94.0 | 75.1 | 77.9 | x86 refs | 15.8 |
| unrelated program (r2 → other) | 100.1 | 100.1 | 104.4 | 107.8 | raw CDC | 0.0 |

* Unrelated programs: **raw CDC wins** on both formats (100.1%; normalization costs 104-113% because its reference metadata, 10-15% of the file, has nothing to deduplicate). A first build (no base)
  is the same case: normalization is a pure overhead until a base exists.
* A tiny body change or an in-package reorder: section + CDC wins by 1-9 points; masking adds a metadata stream to a layout that barely moved.
* Layout-shifting changes (add/remove function, data table, new packages, early insert): normalization wins by 16-39 points, and the **best class set differs by pair**: `full` wins by up to 1.9 points on append-style changes, `x86 refs` wins by up to 4.7 points under movement and new packages/data,
  because classes whose targets have unstable identity (data pointers into a merged section, symbol-less data) add metadata faster than they remove churn. A per-class decision is needed,
  not a per-binary one.
* The decision needs the base and costs one normalization pass per candidate; it is a lab rule, not a design.

## M. Representation cost and performance (PE)

##### representation size of one build (r2)

| flags | file | payload | meta (refs+units+regions) | meta DEFLATEd | refs | units | meta % of file | objects (CDC) |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| std | 6379520 | 6379520 | 651404 | 255033 | 85246 | 5736 | 10.2% | 100 |
| strip | 4353024 | 4353024 | 636006 | 264551 | 94359 | 4162 | 14.6% | 70 |

##### performance, r2 std (best of 3)

| step | MiB/s |
|---|--:|
| parse (container + pclntab) | 1910 |
| x86 + pointer scan | 281 |
| normalize (given slots, units) | 142 |
| reconstruct | 1755 |
| SHA-256 verify (reference) | 1406 |

normalize allocates 55.7 MiB for a 6.1 MiB image (27 streams)

Reference metadata is 10.2% (std) / 14.6% (strip) of the file raw, 4.0% / 6.1% DEFLATEd. PE refs are more numerous than ELF's (94 k vs 79 k) and the section list is searched linearly in `rvaOff`; neither was optimised. Timings vary by about ±30% between runs on this machine.

## Answers

1. **PE structural segmentation:** none (`sect+CDC` ≈ raw CDC; whole sections 99.9% new).
2. **Base relocations:** 1.6 CDC / 4.3 CDC-4K points on append-style changes, nothing under movement; redundant with the pointer scan that serves static ELF.
3. **Instruction references:** the dominant class: stripped add-function 77.5 → 40.7 CDC, 55.6 → 22.4 CDC-4K; +relocations +tables → Δ 3.5 → 2.2.
4. **Same substrate, unchanged:** yes. One scan, one engine, one Go function-table reader across ELF/PE (and a Mach-O view); the parser is the only PE-specific code.
5. **Truly shared:** `Image/Region/Partition`, `Slot{Off,Enc,Bias,Class}`, units, `Normalize/Reconstruct`, `ScanX86`, `ScanPtr`, `gopcln`. PE-only: `EncRVA32`. Deleted: `EncAbs32`, `Slot.Tag`.
6. **Stripped:** both formats keep the Go function table, so units survive; PE stripped Δ 3.5 → 2.2, ELF 3.4 → 0.3 (single `.rdata`, `.reloc`).
7. **Exact reconstruction:** every row is rebuilt from its streams and compared by length, SHA-256 and bytes; `z3bin verify` over the whole corpus (99 binaries, 455 MiB: Go ELF amd64/arm64, PIE, Go PE, Go Mach-O, gcc/clang C) x 5 representations: 495/495 exact.
8. **Incremental cost vs CDC:** stripped add-function PE 77.5 → 41.6 (production CDC), 55.6 → 18.6 (CDC-4K), 3.5 → 2.2 (Δ); std 83.3 → 55.8 / 64.2 → 36.3 / 27.3 → 25.9 (DWARF-bound).
9. **CPU / metadata:** same order as ELF (table M; ±30% run to run); metadata 10-15% of the file raw (4-6% DEFLATEd).
10. **Production integration:** worth a later design **only** together with (a) a finer chunking/delta layer than 64 KiB-average CDC — the gain is real in every representation and mostly unrealised by production CDC — and (b) per-class admission. Not as a standalone transformation of the executable.

## Gate

* reproducible PE families; bounded PE parser with deterministic malformed tests and one smoke fuzz; exact reconstruction everywhere: yes;
* PE relocation normalization measured; common x86 reference code reused (no PE decoder); equivalent ELF/PE mutations compared; substrate simplified on evidence; admission/fallback measured: yes;
* no production persistence changed.

## Limits

* One program family (Go) on three container formats and a small gcc/clang C family on ELF only. No MSVC/MinGW PE and no signed PE: the certificate boundary is tested synthetically only.
* PE32 (32-bit) and ARM64 PE are not parsed; `windows/arm64` was not built.
* The `.rdata` explanation of the ELF–PE gap is supported by attribution, not ablated. `.reloc` prediction, ARM branch/ADRP decoding, DWARF offsets, `st_name` offsets are not done.
* Admission uses a measured cost on the pair at hand (a lab rule), not a feature predictor.

## Tests actually run

Focused during development: `go test ./pez3` (good file; certificate stays opaque; rebasing leaves normalized `.rdata` unchanged; 24 malformed/overflow/offset cases; import-thunk amplification), `./binref`,
`./elfz3` (ELF relocation-section amplification added), `./gopcln`, `./cmd/z3bin`. One 25 s smoke fuzz of `pez3.FuzzParse` (860 k executions, no failure), and the earlier 25 s ELF smoke fuzz; the
fuzz review also produced two bounded-work fixes found by reasoning rather than by the fuzzer (overlapping relocation / symbol sections in ELF, descriptors sharing one thunk array in PE), each with a deterministic test.
`z3bin verify` (495 reconstructions). Final, once: `gofmt -l .` (clean), `go vet ./...` (clean), `go test ./...`.
No DEFLATE, tar, Safetensors or tensor fuzzing; no root ZeroS3 tests (`zeros3.go` and the root module are untouched).
