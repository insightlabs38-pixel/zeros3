set -e
export D=/root/p7/corpus; source /home/user/zeros3/testing-harnesses/z4/b5/families.sh
B=/root/p7/bin; O=/root/p7/out/model2; mkdir -p $O; TIMEFORMAT='%R real %U user %S sys'
S=/home/user/zeros3/testing-harnesses/z4/p7/scripts/bench_model.sh
ver() { python3 -I - "$1" <<'PY'
import hashlib, os, sys
bad = n = 0
for f in os.listdir(sys.argv[1]):
    h = hashlib.sha256()
    with open(os.path.join(sys.argv[1], f), 'rb') as fh:
        while (b := fh.read(1 << 22)): h.update(b)
    n += 1; bad += h.hexdigest() != f
print('verify states %d bad %d' % (n, bad)); sys.exit(1 if bad else 0)
PY
}
p7run() { # name flags... -- files
  n=$1; shift; fl=""; while [ "$1" != "--" ]; do fl="$fl $1"; shift; done; shift
  rm -rf tmp/st_$n tmp/rp_$n
  { time $B/z4p7 family -store tmp/st_$n -json $O/$n.json $fl "$@" ; } > $O/$n.enc.txt 2>&1
  { time ( cd / && $B/z4p7 replay -store /root/p7/tmp/st_$n -out /root/p7/tmp/rp_$n ) ; } > $O/$n.dec.txt 2>&1
  ver tmp/rp_$n >> $O/$n.dec.txt; rm -rf tmp/rp_$n
}
# 1. SmolLM2 with every earlier state as a candidate (B5 pool3 selects its three nearest by content)
p7run smollm2_pool8 -policy pool:8 -- $fam_smollm2
$B/z4p7 ranges -store tmp/st_smollm2_pool8 -orig $D/smollm2/kd_alpaca.safetensors > $O/smollm2_pool8.ranges.jsonl
{ time ( cd / && $B/z4p7 replay -store /root/p7/tmp/st_smollm2_pool8 -workers 4 ) ; } > $O/smollm2_pool8.dec4.txt 2>&1
rm -rf tmp/st_smollm2_pool8
# 2. worker scaling on one pair (encode) and one state (decode)
for w in 1 2 4; do
  rm -rf tmp/st_w; { time $B/z4p7 family -store tmp/st_w -policy prev -workers $w $D/smollm2/base.safetensors $D/smollm2/instruct.safetensors ; } > $O/scale_enc_w$w.txt 2>&1
  { time ( cd / && $B/z4p7 replay -store /root/p7/tmp/st_w -workers $w ) ; } > $O/scale_dec_w$w.txt 2>&1
done; rm -rf tmp/st_w
# 3. estimator regret: sampled planner vs exhaustive oracle (codes every candidate in full), Pythia-14M
p7run p14_sampled -policy pool:3 -- $fam_pythia14m; rm -rf tmp/st_p14_sampled
p7run p14_exhaustive -policy pool:3 -exhaustive -- $fam_pythia14m; rm -rf tmp/st_p14_exhaustive
p7run p14_disc -policy pool:3 -disc -- $fam_pythia14m; rm -rf tmp/st_p14_disc
# 4. structured controls (B5 synthetic controls regenerated with the B5 tool)
[ -d $D/controls ] || $B/z4b5-main controls $D/smollm2/base.safetensors $D/controls > $O/controls_gen.txt 2>&1
p7run ctl_plain -policy pool:8 -- $fam_smollm2ctl; rm -rf tmp/st_ctl_plain
p7run ctl_structured -policy pool:8 -structured -- $fam_smollm2ctl; rm -rf tmp/st_ctl_structured
# 5. Qwen2.5-0.5B
p7run qwen_pool8 -policy pool:8 -- $fam_qwen05; rm -rf tmp/st_qwen_pool8
p7run qwen_structured -policy pool:8 -structured -- $fam_qwen05
$B/z4p7 ranges -store tmp/st_qwen_structured -orig $D/qwen05/abliterated.safetensors > $O/qwen_structured.ranges.jsonl; rm -rf tmp/st_qwen_structured
# 6. object above 1 GiB (synthetic control): two 1280 MiB files
$B/z4p7 gen-big tmp/big 1280 > $O/big_gen.txt
p7run big -policy prev -- tmp/big/a.safetensors tmp/big/b.safetensors; rm -rf tmp/st_big tmp/big
# 7. B5 baseline on Qwen (time / RSS / bytes), unmodified main build
rm -rf tmp/st_b5q; { time $B/z4b5-main family -policy pool3 -rangeseg 65536 -store tmp/st_b5q $fam_qwen05 ; } > $O/b5_qwen_pool3.enc.txt 2>&1
{ time ( cd / && $B/z4b5-main replay -store /root/p7/tmp/st_b5q -out /root/p7/tmp/rp_b5q ) ; } > $O/b5_qwen_pool3.dec.txt 2>&1; rm -rf tmp/st_b5q tmp/rp_b5q
echo done > $O/done
