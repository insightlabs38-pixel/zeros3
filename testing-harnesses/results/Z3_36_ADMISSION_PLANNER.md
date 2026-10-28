# Z3-36 — lineage/admission planner

Status: IN PROGRESS; base `4d2f9593317804a1a8deaab5ca34f5124fb4d735`.
Production diff empty; no persistent planner primitive, global mutable IDs or
production admission policy. No readiness claim before unified evidence.

Checkpoint 1: durable candidate schema names exact root/dependency object IDs,
raw/packed byte costs, complete-input proof hash, bounded samples and analysis/
read costs. Validation rejects missing root cost, foreign sample IDs, duplicate
objects, unordered closure IDs, missing CDC fallback and count/size violations.
Immutable membership bitsets price full missing dependency closures, so a
representation base declined earlier is never available for free later.
Policies: greedy, fixed10% premium cap, late bootstrap, fixed-prior bounded
expected value, one-step buffered lookahead, width8/depth2 beam, exact bounded
B&B hindsight for small cases. Streaming policies inspect only current/past
candidates; lookahead explicitly records future input bytes and buffer size.
CPU metrics exclude hindsight search; search caps cannot claim exactness.
Hand-checked lock-in: CDC100+100 versus rep130+5; oracle135, greedy200,
regret65. Late bootstrap after CDC100 charges missing base110+delta5, never5.
Canonical tests prove prefix decisions do not inspect future rows, root/sample
cost validation and non-exact cap labeling. All pass; no failures removed.
Unified pinned/producer candidate capture, corruption/serialization replay,
policy regret/read/CPU metrics and production design decision remain pending.
