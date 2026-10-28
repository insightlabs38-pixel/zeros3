"""Prepare hash-pinned broad-tail assets/tools, reusing Q15's pinned source trees.
Usage: ROOT CACHE. Producer Python wheels are pinned by python-install.json;
install those exact versions into CACHE/q16-python before running native producers.
"""
import pathlib,subprocess,sys
root,cache=map(pathlib.Path,sys.argv[1:]);pins=root/'testing-harnesses/z3/testdata/q16-tail';download=cache/'q16-download'
subprocess.run([sys.executable,str(root/'testing-harnesses/scripts/q16_fetch.py'),str(download),str(pins/'public.json')],check=True)
tools=cache/'q16-tools'
if not (tools/'usr/bin/7zz').exists():subprocess.run(['dpkg-deb','-x',str(download/'7zip_22.01+really26.01+dfsg-0+deb12u1_amd64.deb'),str(tools)],check=True)
assert '26.01' in subprocess.check_output([str(tools/'usr/bin/7zz'),'i'],text=True).splitlines()[1]
subprocess.run([sys.executable,str(root/'testing-harnesses/scripts/q15_prepare.py'),str(root),str(cache)],check=True)
print('pinned public assets/native 7zip and release source trees verified')
