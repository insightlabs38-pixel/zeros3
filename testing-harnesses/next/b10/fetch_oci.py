import json, sys, urllib.request
def get(url, tok, accept):
    r = urllib.request.Request(url, headers={"Authorization": "Bearer "+tok, "Accept": accept})
    return urllib.request.urlopen(r, timeout=60)
repo = sys.argv[1]
tok = json.load(urllib.request.urlopen(f"https://auth.docker.io/token?service=registry.docker.io&scope=repository:{repo}:pull"))["token"]
for tag in sys.argv[2:]:
    idx = json.load(get(f"https://registry-1.docker.io/v2/{repo}/manifests/{tag}", tok, "application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json"))
    m = [x for x in idx["manifests"] if x.get("platform", {}).get("architecture") == "amd64" and x["platform"].get("os") == "linux"][0]
    man = json.load(get(f"https://registry-1.docker.io/v2/{repo}/manifests/{m['digest']}", tok, m["mediaType"]))
    print(tag, [(l["digest"][7:19], l["size"]) for l in man["layers"]])
# fetch: python3 oci.py REPO TAG... with FETCH=OUTDIR and LAYER=index downloads that layer of each tag
import os
if os.environ.get("FETCH"):
    for tag in sys.argv[2:]:
        idx = json.load(get(f"https://registry-1.docker.io/v2/{repo}/manifests/{tag}", tok, "application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json"))
        m = [x for x in idx["manifests"] if x.get("platform", {}).get("architecture") == "amd64" and x["platform"].get("os") == "linux"][0]
        man = json.load(get(f"https://registry-1.docker.io/v2/{repo}/manifests/{m['digest']}", tok, m["mediaType"]))
        l = man["layers"][int(os.environ["LAYER"])]
        out = os.path.join(os.environ["FETCH"], f"{repo.split('/')[-1]}-{tag}.layer{os.environ['LAYER']}.tar.gz")
        data = get(f"https://registry-1.docker.io/v2/{repo}/blobs/{l['digest']}", tok, "*/*").read()
        import hashlib
        assert "sha256:" + hashlib.sha256(data).hexdigest() == l["digest"]
        open(out, "wb").write(data)
        print(out, len(data))
