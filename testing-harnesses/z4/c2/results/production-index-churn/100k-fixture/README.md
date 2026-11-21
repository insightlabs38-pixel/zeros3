# Published 100,000-object index

The fixture uses 100,000 acknowledged signed S3 PUTs of unique
256-byte bodies. A fresh process checked 102 sampled bodies
independently. C2B published 100,000 live index identities and
1,600,000 postings across seven runs. Inspect verified the current
generation, and deep verify found 100,000 exact chunks and zero issues.
The index occupied 22,282,240 allocated bytes. This is a controlled
compact-object scale check, with ten-million-object qualification still open.

The input fixture took about 484 seconds to write while qualification
and large fixture creation were active. Index publication took 637.99
seconds elapsed, including 103.18 user and 19.41 system CPU seconds.
