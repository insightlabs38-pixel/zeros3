#!/usr/bin/env python3
"""Regenerate the IQ3_S quantizer block of zeros3.go section 16g from testing-harnesses/z3/qcore/iq3.go (same renaming
as the P7 extractor: unexported names get the nc prefix with an underscore, exported ones the prefix alone).
usage: port_iq3.py   (from the repository root; rewrites zeros3.go in place, then run srcmap.py)"""
import re
src = open('testing-harnesses/z3/qcore/iq3.go').read()
src = src[src.index('// IQ3_S (ggml'):]
ren = {k: 'ncQcore_' + k for k in ['kgrid512', 'iq3Tables', 'iq3Once', 'iq3Tab', 'iq3Init', 'iq3Scratch', 'iq3Find', 'iq3Ni', 'rowIQ3S', 'iq3FMA', 'iq3Mask', 'iq3Abort',
                                    'fma32', 'mul', 'madd', 'sqrt', 'nearestInt', 'clamp', 'put16'] + ['fx3' + n for n in ['Sumx2', 'Sigma', 'Scale', 'Round', 'DistAcc', 'Dist', 'Qx', 'Q2', 'All']]}
ren['F16'] = 'ncQcoreF16'
ren['ErrAbort'] = 'ncQcoreErrAbort'
def sub(s):
    for k, v in sorted(ren.items(), key=lambda kv: -len(kv[0])):
        s = re.sub(r'(?<![A-Za-z0-9_.])' + k + r'(?![A-Za-z0-9_])', v, s)
    return s
new = sub(src).rstrip() + '\n\n'
z = open('zeros3.go').read()
a = z.index('// IQ3_S (ggml quantize_iq3_s')
b = z.index('// Type is the ggml tensor type id.\ntype ncQcoreType uint8')
z = z[:a] + new + z[b:]
open('zeros3.go', 'w').write(z)
