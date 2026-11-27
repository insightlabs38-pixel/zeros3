# GF1 constrained candidate selection

For each exact root choose one feasible immutable reconstruction program.
A program includes its entire dependency closure and charges only reachable
records. Solve first with fixed physical placements and fixed direct parents
to make a small oracle exact, then include mutable intermediate choices and
placement as search variables. Variable choices must maintain a DAG and native
per-parent identity. Any cap measured only in a model needs physical replay.

Compare independent selection, one-base selection, bounded cohort activation
with coordinate exchanges, width-bounded prefix search, and the current
native global selection. Reprice the native selection only if it belongs to
the same restricted catalog. Include the full native archive comparator
from GF0. The initial hard read caps are 1×, 2× and 8× the worst current CDC
probe and whole decoded bytes for each root. These are work limits.

Prediction. A shared dependency or an avoided pack allocation can make
an otherwise unprofitable pair profitable. The real native graph may already
select this pair. A gain beyond the incumbent then needs measured physical
read/pack interactions or a new admissible edge from GF2/GF3. Report ties
and catalog mismatches. Precommit to checking allocation and live reads
after native materialization before drawing physical conclusions.
