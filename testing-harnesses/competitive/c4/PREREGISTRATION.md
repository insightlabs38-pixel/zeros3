# C4 · Default S3 and operational qualification

C2's 144-request samples suggested a 5–10% default S3 throughput decline.
Test whether matched longer pairs reproduce it using frozen B10 and C2 on
the same two-CPU quota and default configuration. Use identical generated
payloads spanning 1 KiB to 1 MiB and alternate binary order. Keep one fresh
store at a time. Separate a post-restart first pass, kernel cache uncontrolled,
from warm GET and range lanes. Record median pair ratios and resampled
intervals, p95/p99, process CPU, RSS and disk I/O. A persistent ≥5% paired
loss whose interval excludes zero warrants one bounded source/profile
investigation. Apply a fix only when both the causal path and benefit hold.

Smoke the documented single-owner operational contract on disposable
stores, using two actual S3 clients and one backup workflow. Verify all
restored objects by digest. Prioritize B10 stale group handles, C2 archive
finalization, C3 import, pack recovery and journal ordering with focused
tests and bounded crash cuts. Use the existing T1 runner for a resumable
partition; record both selected and omitted scopes. Preserve the pre-1.0
compatibility floor and fail-closed behavior.

Cross-build Linux ARM64 with CGO disabled. If an authentic emulator is
available, execute a compact protocol, authority and recovery check on it
and record the exact emulator and kernel context. Keep native ARM and
physical power-loss qualification as independent future measurements.
