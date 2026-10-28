# Z4 A2 Model production — active checkpoint

A2 active, not READY. Base main 710248c; drafts #80/#81 remain unchanged.
Mechanically extracted only Safetensors/GGUF parsers, Model decoder/residual
helpers and bounded Model analyzer from #80. DEX/CLR parsers, codecs and the
quadratic managed index helper are excluded. Clean extraction builds.
Source block hashes/line counts are retained; LOC is not a promotion criterion.

A1's real matrix selected cumulative format floors. The Model publication floor
and supported ceiling seven must be activated here and verified on this actual
clean production candidate. No production merge until all A2 gates pass.

Independent real family pinned before model latency: sentence-transformers/
all-MiniLM-L6-v2 revision 1110a243fdf4706b3f48f1d95db1a4f5529b4d41,
90,868,376 bytes, SHA256 53aa51172d142c89d9012cce15ae4d6cc0ca6895895114379cacb4fab128d9db.
Download is bounded to 100 MiB and producer/download time excluded from analysis.
This broadens the real format/tensor cohort; it is not an invented fine-tune history.

Pending: controlled aggregate-replay reproduction/defense; malformed recipe and
metadata tests; bounded residual/hop checks; source-prune/base-liveness lifecycle;
actual old/new operation/crash matrix; independent-family exact full/range reads;
separate analysis/proof/decode/RSS including retained 1 GiB behavior; final review.

Second checkpoint: floor seven is active in the clean candidate; actual old/new
operation, snapshot, portable bundle and five SIGKILL matrices passed. Model
frozen hop vectors, five-revision lifecycle, GGUF/fallback and source-prune/GC/
repack liveness pass. Retained 1 GiB control: 75.27 MiB peak heap growth, 10.968 s
analysis+proof (combined test timing, not isolated throughput).

A small repeated empty-output DAG exhausted the test safety ceiling after
600,001 reads under the original decoder; no existing production limit fired.
The preserved failing case now returns errRepLimit after about 209,441 reads.
Aggregate steps/metadata/intermediate bytes are shared across nested pipe readers;
size reservations cannot overflow. Empty components still verify presence/SHA.
A range-accounting correction charges actual windows rather than whole tensor
sizes; a 16 MiB tensor/window-loop regression passes. Full root suite is pending;
final latency/fuzz/race/integration review remain gates, not completion claims.
