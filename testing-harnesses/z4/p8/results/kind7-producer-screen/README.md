# Kind 7 producer and nesting comparison

The archived Go 1.27.0 P8 test binary and the consolidated P8R probe used
the same input bytes. Build information and exact executable SHA-256
are recorded. These test executables do not embed a Git revision.

Each build replayed two exact repetitions of a zstd 1.5.7 level-3
coreutils 9.7 tar, two pinned reference streams (zstd 1.5.5 and 1.5.7)
and a nested stream. The compressed coreutils tar expands to the same
independently checked SHA-256 at levels 3 and 19. Both builds declined
the level-19 input because its 7,451,045-byte description exceeded the
6,732,207-byte source. The candidate child reports this refusal as a
test failure and the driver does not publish its JSONL. Raw evidence
for both declined trials is preserved.

For the admitted inputs, representation sizes agreed across builds.
The level-3 tar and the short reference streams grew, while the nested
stream fell from 742,891 to 570,793 representation bytes. The latter
size is from the representation probe and excludes production store and
maintenance costs. CPU and RSS are exploratory, with concurrent
qualification and no guarantee of comparable scheduling.
