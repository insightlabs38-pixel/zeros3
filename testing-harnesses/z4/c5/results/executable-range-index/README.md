# Indexed replay of normalized executable deltas

The existing standalone normalized exact-delta trial now writes a small index of the original normalized streams. Range reconstruction decodes only overlapping masked payload and reference streams, plus the unit table if needed. Independently checked 4 KiB ranges included two shipped amd64 Go executables across releases 1.27.1 and 1.27.4 as storage inputs.

The normalized compile trial's six ranges took 3.2–92.3 ms, with a median near 18.5 ms. The previous full-reconstruction path on that same trial took 416 ms for its midpoint sample. The matching raw delta took 2.6 ms for the midpoint. The index added 2,312 bytes to the normalized stored basis and delta. It leaves the normalized basis fully loaded and some text ranges still read an entire reference stream. This is laboratory replay without a new production representation.
