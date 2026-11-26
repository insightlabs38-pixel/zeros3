# Reproduce FB-07

Run the focused physical fixture using Go 1.27.0. It writes
ordinary native raw packs, keeps a separate healthy duplicate,
and flips known bytes in the disposable fixture.

```sh
GOMAXPROCS=2 go test -v -count=1 -timeout=75s -run '^TestFB07' .
"$BIN" scrub -store "$STORE" -max-mib 128 -workers 2
# Continue from the previous report's last field when next is nonempty.
"$BIN" scrub -store "$STORE" -max-mib 128 -workers 2 -after "$LAST"
```

After a scan reports issues, the command emits JSON and returns
exit code 1. To use a cursor, preserve the same sealed pack
inventory between invocations.

For physical repair, save the complete JSON report and stop
writers. Use `repair -store "$STORE" -scrub-report "$REPORT"`.
Add `-from "$PEER"` with the authorized peer's signing configuration
if a record has no usable local copy. The repair fixture uses a
disposable loopback peer. The process-cut test runs this offline
entry point in a child before letting the predecessor be replaced.

For the sealed raw carry, the source must be a checked native
raw pack with live and dead records. `TestFB07SealedRawCarry`
exercises the actual offline repack entry point against clean
source data, a staged-file change and a compressed-source control.
For an offline disposable store, use
`repack -apply -compression off -verified-raw-carry -json`
and compare the writer and seal payload counters.
