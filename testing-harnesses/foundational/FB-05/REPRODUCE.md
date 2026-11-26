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
