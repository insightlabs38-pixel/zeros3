# GF7 token translation across compressed generations

GF2 independently compiled compressed bit spans. It succeeded on one
TypeScript pair and lost most usable spans on its adjacent release and
urllib3. Test whether source and target still share LZ token sequences
after changing Huffman tables. Read the original compressed source at
indexed token boundaries, then emit the target's exact codewords without
re-running a plaintext match finder.

Compare two executable variants. The first copies unchanged token
sequences through target code tables. The second uses matches that share
literal/length symbols and corrects the exact distance field. Charge the
actual source bytes, tables, syntax programs, checkpoints, raw fallback
and pack alignment. Include all target headers, EOB bits and tail padding.

A match can save at most the number of target code bits it covers before
its selector, source offsets, corrections and checkpoints. Measure that
coverage and its practical gap against GF2, CDC and the archive. Independent
source ranges and block digests must verify before returning any bytes.
A changed compressor and unrelated content are adverse controls.
