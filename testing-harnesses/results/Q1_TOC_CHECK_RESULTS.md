# Q1 source-map / test-map check results

**Harness:** `harness/toc_check` (static, dependency-free; no `zeros3`
binary, no network)
**Tested against zeros3 commit:** `c1a7135c15d12de91612d8e3d064bc982343de65`
(branch `claude/zeros3-q1-cleanup-elmbzf`, the Q1 candidate -- the exact
commit Q1 added zeros3.go's "Source map" and zeros3_test.go's "Test map"
comment blocks in)
**Result:** **PASS on both files**

```
$ go run . -file /path/to/zeros3/zeros3.go
PASS: Source map in /path/to/zeros3/zeros3.go -- all 26 entries point at their claimed lines

$ go run . -file /path/to/zeros3/zeros3_test.go
PASS: Test map in /path/to/zeros3/zeros3_test.go -- all 24 entries point at their claimed lines
```

## What this checks

Q1 added a top-of-file comment block to each file (`"Source map"` in
zeros3.go, `"Test map"` in zeros3_test.go) listing every major
subsystem/test-family alongside the exact line number its section
begins on. A line-number TOC like this goes stale silently: nothing
about the Go compiler, `gofmt`, or the test suite would notice if a
later hand-edit shifted a section without updating the map above it.

`toc_check` parses the map's `//   LINE    Description` rows and, for
every entry after the first, confirms that `LINE` is immediately
preceded by the project's own `// ===...===` section-divider convention
-- i.e. that `LINE` really is where a named section starts, not just
some line that happens to still exist. The first entry (which anchors
the top-of-file package doc in zeros3.go, and TestMain's doc comment in
zeros3_test.go -- neither is a divided section header) is checked only
for being non-blank and in range. Entries must also appear in strictly
increasing line order.

This was verified to actually catch drift, not just report a trivially
true result: pointed at a copy of zeros3.go with one comment line
inserted above the CDC section without updating the map, `toc_check`
correctly failed on 25 of 26 entries (everything from the shifted
section onward), naming each claimed line and what was actually found
there instead of the expected divider.

## What this does and doesn't prove

Proves: as of the tested commit, every one of the 26 (zeros3.go) + 24
(zeros3_test.go) map entries is exactly accurate.

Does not prove: that the map's *description* text is a good summary of
what's actually in that section (this is a structural/line-accuracy
check, not a semantic one) -- that judgment call was made by hand
during the Q1 pass and is the reviewer's own to re-check by reading the
map against the file.

## Notes on scope

This harness never builds or runs a `zeros3` binary, never opens a
socket, and has no import on anything AWS-SDK-related -- unlike every
other harness in this repository, it is pure static analysis over the
source text, so it has no bearing on (and proves nothing about) wire
protocol, storage, or CLI behavior. It exists solely because Q1 is an
editorial/navigability pass where the main foot-gun is exactly this kind
of line-number drift, not a runtime regression. See `zeros3`'s own
`STATUS.md` ("Q1" section) for the wire-behavior/persistence/regression
evidence (internal test suite, race/vet/gofmt, reproducible build, and
an external harness spot-check) that this file's counterpart harnesses
already established are unaffected by Q1's comment-only diff.
