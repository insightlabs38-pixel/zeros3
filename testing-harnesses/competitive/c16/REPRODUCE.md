# Reproduce C16

Build Go 1.27.0 engine `a0ab61b` and the current engine. Use the C1 Python
environment, C2 verified TypeScript releases and the C14 finalized
corpus. All endpoints and credentials are disposable loopback fixtures.
Use the pinned OpenXet executable from C1 for the independent lane.

```bash
common=(--binary "$C16_BINARY" --downloads "$C2_DOWNLOADS" --c1 "$C1_CORPUS" --c14 "$C14_CORPUS")
"$PYTHON" "$HARNESS/../c3/transfer.py" --binary "$C15_BINARY" --downloads "$C2_DOWNLOADS" \
  --corpus "$C1_CORPUS" --scratch "$WORK/c3" --results "$WORK/typescript-baseline.jsonl" \
  --families typescript --states empty same-key different-key same-authority different-authority \
  --lanes s3 replicate closure-cross --finalize
"$PYTHON" "$HARNESS/frontier.py" "${common[@]}" --families typescript --preparations lean \
  --states same-key different-key --shapes loopback fast-lan wan constrained \
  --work "$WORK/shaped" --results "$WORK/shaped.jsonl"
"$PYTHON" "$HARNESS/frontier.py" "${common[@]}" \
  --families typescript shift entropy sqlite tensor-dtypes mobilenet --preparations none \
  --lanes s3 replicate closure-derived --work "$WORK/controls" --results "$WORK/controls.jsonl"
for peer in source receiver; do
  "$PYTHON" "$HARNESS/frontier.py" "${common[@]}" --"$peer"-binary "$C15_BINARY" \
    --families typescript --states different-key --lanes closure-derived \
    --work "$WORK/old-$peer" --results "$WORK/old-$peer.jsonl"
done
"$PYTHON" "$HARNESS/base_probe.py" --binary "$C16_BINARY" --gzip "$C2_DOWNLOADS/typescript-5.6.2.tgz" \
  --work "$WORK/probe" --results "$WORK/base-probe.jsonl"
"$PYTHON" "$HARNESS/openxet.py" --binary "$OPENXET" --downloads "$C2_DOWNLOADS" \
  --c1 "$C1_CORPUS" --c14 "$C14_CORPUS" --work "$WORK/openxet" --results "$WORK/openxet.jsonl"
go test -run '^(TestC16|TestAuthority(ClosureSyncReplication|ClosureSyncImportRejectsBadPayload|CrossKeyHints|ThinImportNamespaceCrashCuts|ReplicationToNewAndOldDestinations)|TestSync_Discovery_Supported)' .
go test -race -run '^(TestC16|TestAuthorityThinImportNamespaceCrashCuts)' .
```

The source-optimize rows separate wall, CPU, RSS and output blocks from
transfer. A transfer row has signed relay byte counts per direction and
per endpoint, sender, receiver and client metrics, finalization and whole
SHA-256. Sum both directions for each relay leg. The relay paces existing
HTTP request payloads and injects delay at completed request headers.
The SQLite comparison runs `--families sqlite --lanes closure-cross
closure-derived` under the same fixture and finalization.
