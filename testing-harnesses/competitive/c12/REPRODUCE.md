# Reproduce C12

Use Go 1.27.0, C1's Python environment and zstd 1.5.7. Download and
extract xdelta3 3.0.11 and bsdiff 4.3 in scratch. These are independent
command-line references. The Go lab uses the C11 source and standard
library. It does not add a storage format to ZeroS3.

```bash
mkdir -p "$WORK"/{debs,tools,lab}
curl -fLsS https://deb.debian.org/debian/pool/main/x/xdelta3/xdelta3_3.0.11-dfsg-2_amd64.deb -o "$WORK/debs/xdelta3.deb"
curl -fLsS https://deb.debian.org/debian/pool/main/b/bsdiff/bsdiff_4.3-23_amd64.deb -o "$WORK/debs/bsdiff.deb"
(cd "$WORK/debs" && sha256sum -c "$HARNESS/tools.sha256")
for name in xdelta3 bsdiff; do dpkg-deb -x "$WORK/debs/$name.deb" "$WORK/tools"; done
git -C "$REPO" show 498bf8e:zeros3.go > "$WORK/lab/zeros3.go"
git -C "$REPO" show 498bf8e:go.mod > "$WORK/lab/go.mod"
cp "$HARNESS/lab_test.go.txt" "$WORK/lab/lab_test.go"
(cd "$WORK/lab" &&
  GOMAXPROCS=2 go test -run '^(TestC12Integrity|TestC12AnchoredShift)$' &&
  GOMAXPROCS=2 go test -c -o c12.test
)
"$PYTHON" "$HARNESS/fixtures.py" --root "$WORK/corpus" \
  --c1 "$C1_CORPUS" --c5 "$C5_CORPUS" --manifest "$HARNESS/fixtures.json"
args=(--lab "$WORK/lab/c12.test" --tools "$WORK/tools/usr/bin"
      --corpus "$WORK/corpus" --work "$WORK/trials")
training=(typescript-gzip typescript-tar sqlite native tensor shift entropy volume events)
"$PYTHON" "$HARNESS/compare.py" "${args[@]}" --families "${training[@]}" --results "$WORK/final-comparison.jsonl"
"$PYTHON" "$HARNESS/compare.py" "${args[@]}" --families httpx-heldout --pairs 0 --results "$WORK/heldout.jsonl"
native=(--baseline "$C11_BINARY" --work "$WORK/stores" --corpus "$WORK/corpus")
"$PYTHON" "$HARNESS/stores.py" "${native[@]}" --families "${training[@]}" httpx-heldout \
  --prepare --results "$WORK/native-prepare.jsonl"
"$PYTHON" "$HARNESS/stores.py" "${native[@]}" --families "${training[@]}" httpx-heldout \
  --results "$WORK/native-read.jsonl"
```

The C1 and C5 scripts and frozen manifests supply both input trees.
The first XOR and packed experiments can be replayed with the lab at
`a31d240` and the final runner. Keep xdelta3's external decompression
and recompression switches disabled to match the actual source bytes.
