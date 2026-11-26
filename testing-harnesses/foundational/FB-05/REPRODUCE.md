# Reproduce FB-05 byte admission

Use Go 1.27.0. The physical fixture uses generated unique 8 KiB
objects and a 48 KiB compaction target so several packs have
independently deletable records. Data is hashed and verified
after reopen. The online pass uses both a one-byte-short budget
and an exactly fitting budget.

```sh
GOMAXPROCS=2 go test -count=1 -timeout=90s -v \
  -run '^(TestFB05RewriteBudgetRetiresDeadSeparately|TestFB03_MaintIsolatesCorruptRecord|TestB4OnlineReclaimsDeadData|TestB4CancelledPass)$' .
```

To check screened compression, run `TestFB05RawCompressionAdmission`
with the same compiler. The positive source uses distinct numbered
text, the control uses deterministic random bytes, and both start
with all-raw packs. Run both passes and verify from a fresh process.
The positive source also tests an insufficient aggregate scratch
budget followed by verification, reopen and a successful retry.

`TestFB05MultipartExpiryScopeAndRetry` journals fixed initiation
times, verifies a recent part, injects sync ENOSPC and retries
after reopening. The fixture uses both journal formats. The
bucket, prefix, UTC cutoff and default-off controls are verified
independently.
