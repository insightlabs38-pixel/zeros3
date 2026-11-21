# Compact retained-history checkpoint

The existing million-object fixture's journal and V2 checkpoint were
copied into an isolated shadow, with immutable pack files linked for
read-only access. The V3 writer reused the complete journal history.
Both layouts were measured with the same Go 1.27.4 test binary and two
Go processors. The V3 lane temporarily hid V2 in the shadow so a fallback
could not supply the measured checkpoint result.

| Metric | V2 | V3 |
| --- | ---: | ---: |
| File bytes | 176,676,394 | 167,708,949 |
| Mean Go bytes allocated per open | 1,018,477,228 | 861,776,672 |
| Mean Go allocations per open | 7,603,416 | 5,510,700 |

Two warm observations per layout recorded approximately 3.01–4.29 s
for V2 and 2.18–2.61 s for V3. The limited timing sample is recorded
separately from the deterministic state check. V3 reopened the same
701,053 visible and 298,947 retained roots as full journal replay and
matched 200 independent read and absence checks. The before and after
digests of the journal and V2 checkpoint were identical.
