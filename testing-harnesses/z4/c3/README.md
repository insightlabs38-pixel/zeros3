# Native closure relay evidence

The Coreutils screen used the distributed 9.6 and 9.7 gzip release bytes
from the C4 retained history pilot. Each destination started from a clone
of the same single-object store. The seed held the authenticated 9.6
base and its authority record. The source exposed the 9.7 authority
delta. Both commands replaced the same destination key while keeping
its retained 9.6 revision. The run alternated CDC, native, native and CDC.

| Mode | Upload | Allocated bytes after GC and repack | Client CPU, seconds | Elapsed, seconds |
|---|---:|---:|---:|---:|
| CDC | 14.18 MiB | 32,165,888 | 0.096, 0.147 | 0.746, 0.840 |
| Native | 11.02 MiB | 28,913,664 | 0.055, 0.066 | 1.000, 0.736 |

Native upload includes the binary closure and import control header. The
upload figures are the CLI's rounded application payload sizes and
exclude HTTP framing and discovery. Both modes passed independently
hashed S3 readback and full deep verification after maintenance. The
trials shared a two-CPU quota with qualification and fixture writing.
The elapsed results varied. The experiment did not isolate server CPU or
add WAN conditions.

The five-generation focused run exercised the new client's opt-in against
all five historical binaries. It checked standard CDC fallback without a
native probe, exact downloaded bytes, native store refusal before mutation,
and compatible older snapshot bundle imports where supported. A separate
bidirectional pre-authority peer run covered a historical source as well
as a historical destination.

[Per-trial payload, timing and verification](results/coreutils-native-relay.json)

A destination-side loopback proxy limited uploads to 2 MiB/s. The matched CDC and native runs forwarded 14,913,828 and 11,565,721 HTTP body bytes respectively. Native took 6.26 s and CDC 5.30 s in these single runs. After a two-thirds native upload, the server refused the partial closure and a fresh process served the exact old release. With the successful native acknowledgement dropped, a fresh process served the exact new release. Retrying sent only 8,624 control bytes, preserved one current and one historical version and passed deep verification. This is a controlled loopback transfer with a modeled bandwidth limit.

[Shaped transfer and fault observations](results/coreutils-shaped-relay.json)
