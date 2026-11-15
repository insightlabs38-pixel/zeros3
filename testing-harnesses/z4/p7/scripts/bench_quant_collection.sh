#!/bin/bash
# quantized GGUF collection: Safetensors base -> instruct -> f16.gguf -> 13 bartowski (imatrix) + 14 QuantFactory quantizations
cd /root/p7; B=/root/p7/bin; D=/root/p7/corpus; O=/root/p7/out/quant; mkdir -p $O
BW="Q2_K Q3_K_M Q3_K_L Q4_0 Q4_K_S Q4_K_M Q5_K_S Q5_K_M Q6_K Q8_0 IQ4_XS IQ3_XS IQ3_M"
QF="Q2_K Q3_K_S Q3_K_M Q3_K_L Q4_0 Q4_1 Q4_K_S Q4_K_M Q5_0 Q5_1 Q5_K_S Q5_K_M Q6_K Q8_0"
files="$D/smollm2/base.safetensors $D/smollm2/instruct.safetensors $D/quant/bw/f16.gguf"; par="-;0;1"
for q in $BW; do files="$files $D/quant/bw/$q.gguf"; par="$par;2"; done
for q in $QF; do files="$files $D/quant/qf/$q.gguf"; par="$par;2"; done
sha256sum $files > $O/orig.sha256
$B/z4b5-main cdc $files > $O/cdc.txt 2>&1
rm -rf tmp/st_qd; { time $B/z4p7 family -store tmp/st_qd -json $O/direct.json -policy direct $files ; } > $O/direct.enc.txt 2>&1; rm -rf tmp/st_qd
rm -rf tmp/st_qe; { time $B/z4p7 family -store tmp/st_qe -json $O/derived.json -hfmap -aux $D/quant/bw/imatrix.dat -parents "$par" $files ; } > $O/derived.enc.txt 2>&1
{ time ( cd / && $B/z4p7 replay -store /root/p7/tmp/st_qe -workers 4 ) ; } > $O/derived.dec.txt 2>&1
$B/z4p7 ranges -store tmp/st_qe -orig $D/quant/bw/Q4_K_M.gguf > $O/derived.ranges.jsonl 2>&1
python3 -I - $O <<'PY' >> $O/derived.dec.txt
import json,sys,os
O=sys.argv[1]
want={l.split()[0] for l in open(O+'/orig.sha256')}
got={json.loads(l)['sha256'] for l in open(O+'/derived.dec.txt') if l.startswith('{"sha256"')}
print('verify states %d missing %d' % (len(got), len(want-got)))
PY
rm -rf tmp/st_qe; echo done > $O/done
