# C2A standalone release qualification

PR #122 revision 067359776affa86defe29b93a2897226ea5c88a7 passed all 36
mandatory stages with Go 1.27.4 and GOMAXPROCS=4. The source fingerprint
is d9390f73bb7428cc9b43945138d7c212999519583dad4ee953c76ba1838da03a.

The first optimized race run recorded one locality timing failure.
The conventional race run passed that same test. The original result
is retained. A serial targeted continuation passed the failure and
verified equivalent outcomes across 1,337 tests in each mode.

These results cover the exact C2A revision above. The combined P8R
release tree has a different production fingerprint.
