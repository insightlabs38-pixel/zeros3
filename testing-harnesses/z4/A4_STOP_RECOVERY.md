# A4 stop and recovery

Campaign paused at the user's explicit request on 2026-10-29.
Hourly automation 6ae1844e1ae08191b077f0fde95b52ac is disabled.
Active source branch: z4/a4-managed-streaming; PR #95 remains DRAFT.
Production main is e4e5b1b; A0–A3 completed. A4 is first unfinished; B1–B8 queued.
B2 remains DRAFT_ONLY. Drafts #80/#81 are retained separately.

9f164ac preserves the formerly uncommitted production port; this final stop commit
adds final isolated measurements, contract tests, reports and evidence. All latest
production source is now durable; do not use the older runtime-only backup as the
latest source. z4/a4-stop-backup-20261029 preserves this final stop snapshot.

Production source SHA-256:
64e997ef515c1406f328b2a00517180eca3dcf01287deb0d7dc390b05f45621a

See results/Z4_A4_MANAGED_STREAMING.md (relative to testing-harnesses),
a4-production-checkpoint-evidence.json.gz and a4-stop-evidence.json.gz.
Raw measurements remain in /workspace/work/z4-a4-il-final; earlier negatives in
/workspace/work/z4-a4 and z4-a4-io-fixed / z4-a4-memory-final. Do not discard failures.
Binaries, cloned test stores and generated component files are reproducible; scripts,
fixture provenance, input hashes and compressed evidence are committed. No additional
large executable/store backup is needed. No claim of physical storage saving: CDC
remains authoritative and retained; representation growth is additive.

Resume: inspect active workers/tree, fetch origin, inspect main/PRs and reports;
read final test evidence, review complete A4 diff and architecture against campaign
memory/work/exactness and merge gates. Do not restart A0–A3 or mark A4 ready solely
because measurements passed. Keep default off, cumulative floor 9, earlier decoders,
mechanical code reuse and test integrity. Only then decide A4 readiness and whether
its authorized merge gates permit promotion. Continue B1–B8 only when instructed
to resume; never re-enable the hourly task without user authorization.
