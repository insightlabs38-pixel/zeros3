# Related and unrelated tar histories

The smoke used three pinned GNU Coreutils source tarballs and an
independently produced SQLite 3.49.1 amalgamation tar. The original
SQLite files were the pinned B4 inputs. GNU tar sorted entries, used
a fixed UTC timestamp and numeric zero ownership, and wrote GNU format.

| Migration | Ordinary allocated bytes | After explicit recompression |
| --- | ---: | ---: |
| Existing candidate discovery | 129,200,128 | 40,185,856 |
| Persistent index for all archives | 70,881,280 | 25,690,112 |
| Persistent index for Coreutils, SQLite in CDC | 79,765,504 | 26,943,488 |

The persistent index found an older Coreutils base for the related 9.7
target. The distinct SQLite source family still shared enough exact spans
with the Coreutils data to admit an archive delta. It chose an older
depth-zero base under indexed discovery and used about 1.25 MB less after
recompression than the scoped lane that kept SQLite in CDC. The measured
read cost for SQLite remained several times its CDC estimate. Both
unscoped lanes matched four independently hashed signed GETs and every
lane passed deep verification under both maintenance states.

[Raw plans and exact readback](trials.json)
