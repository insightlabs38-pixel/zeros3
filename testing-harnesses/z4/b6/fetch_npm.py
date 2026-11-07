#!/usr/bin/env python3
"""Fetch pinned npm package tarballs (real release histories) and extract *.woff2 members.
usage: fetch_npm.py CACHE pkg version [version...]  -> CACHE/npm/<pkg>/<ver>/<member>"""
import sys, os, json, urllib.request, tarfile, io, hashlib
cache = sys.argv[1]; pkg = sys.argv[2]; vers = sys.argv[3:]
meta = json.load(urllib.request.urlopen("https://registry.npmjs.org/" + pkg.replace("/", "%2f")))
for v in vers:
    d = os.path.join(cache, "npm", pkg.replace("/", "_"), v)
    if os.path.isdir(d): continue
    info = meta["versions"][v]["dist"]
    data = urllib.request.urlopen(info["tarball"]).read()
    tf = tarfile.open(fileobj=io.BytesIO(data))
    os.makedirs(d + ".tmp", exist_ok=True)
    for m in tf.getmembers():
        if m.isfile() and m.name.endswith(".woff2"):
            out = os.path.join(d + ".tmp", m.name.replace("package/", "", 1).replace("/", "__"))
            open(out, "wb").write(tf.extractfile(m).read())
    os.rename(d + ".tmp", d)
    print(pkg, v, len(os.listdir(d)), "woff2", info.get("shasum"))
