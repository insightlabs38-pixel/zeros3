# ZeroS3 Grand Frontier · GF0–GF8

Initial engine checkpoint `3db525631727c38f548420e323038ab31e047080`.
All experiments use disposable stores. Planned production changes require
separate physical, byte-exact, retention, compatibility and recovery evidence.

- [Program ledger](LEDGER.md)
- [Prior art and experiments](PRIOR_ART.md)
- [GF0 native baseline and engine map](GF0/README.md)
- [GF1 constrained planner and first exact oracle](GF1/README.md)
- [GF2 compressed query compilation](GF2/README.md)
- [GF3 multi-source discovery and phrase banks](GF3/README.md)
- [GF4 workload and maintenance policy](GF4/README.md)
- [GF5 exact generational checkpoint windows](GF5/README.md)
- [GF6 receiver-aware exact transfer](GF6/README.md)
- [GF7 compressed-domain token translation](GF7/README.md)

The baseline corpus manifest pins exact input sizes, digests, order and splits.
The archive minimum-space lane and CDC fast lane both remain relevant
comparators. Evaluate any candidate on the space/read frontier under the
same dependency, finalization, and verification rules.
