# C18 · Recovery and portable consumers

Freeze 220a336 as the control. Reuse the foundational journal watermark,
strict recovery and checkpoint salvage evidence already on this branch.
Exercise the changed C13/C15/C16/C17 boundaries with bounded process cuts,
verified reopen, and independently exact restores.

Investigate local recovery of a pack with one damaged preset context.
Reconstruct the damaged record from its independent verified copy. Check
the entire staged file against the original pack identity before an atomic
replacement under exclusive store access. Compare it with ordinary
repack, and preserve the original on missing copies or unrelated damage.

Measure rewritten bytes, CPU, recovery time, and ordinary read behavior
for real source-fragment packs. Exercise official AWS Go SDK, Boto3 and
Restic. Build static Linux/arm64 and run an exact QEMU smoke with the
same fixtures where practical. Distinguish process cuts from physical
power loss and emulated execution from native ARM performance.
