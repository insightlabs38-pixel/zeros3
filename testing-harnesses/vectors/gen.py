#!/usr/bin/env python3
"""Reference SigV4 for S3, written from the AWS documentation using only the
Python standard library -- independent of ZeroS3's Go signer.

  python3 gen.py            rewrite sigv4.json and presign.json (provenance only;
                            the Go tests never run this)
  python3 gen.py --check    recompute everything and compare with the committed
                            files (and with AWS's published example signatures)

A native client (e.g. Mojo) can port `sign()` / `presign()` and test itself
against sigv4.json and presign.json before talking to any server.
"""
import hashlib, hmac, json, os, sys
from urllib.parse import quote

ACCESS, SECRET = "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
HERE = os.path.dirname(os.path.abspath(__file__))


def sha256(b): return hashlib.sha256(b).hexdigest()
def hm(k, m): return hmac.new(k, m.encode(), hashlib.sha256).digest()
def enc(s): return quote(s, safe="-_.~")  # S3 UriEncode (no slash exemption: caller splits)


def canonical_uri(raw_path):
    # S3: each path segment is decoded then encoded exactly once; slashes stay.
    from urllib.parse import unquote
    return "/".join(enc(unquote(seg)) for seg in raw_path.split("/"))


def canonical_query(raw_query):
    from urllib.parse import unquote
    pairs = []
    for p in filter(None, raw_query.split("&")):
        k, _, v = p.partition("=")
        pairs.append((enc(unquote(k)), enc(unquote(v))))
    return "&".join(f"{k}={v}" for k, v in sorted(pairs))


def signing_key(date, region):
    return hm(hm(hm(hm(("AWS4" + SECRET).encode(), date), region), "s3"), "aws4_request")


def sign(method, raw_uri, headers, payload_sha, amz_date, region, signed):
    path, _, query = raw_uri.partition("?")
    low = {k.lower(): " ".join(v.split()) for k, v in headers.items()}
    names = sorted(signed)
    canon = "\n".join([method, canonical_uri(path), canonical_query(query),
                       "".join(f"{n}:{low[n]}\n" for n in names), ";".join(names), payload_sha])
    scope = f"{amz_date[:8]}/{region}/s3/aws4_request"
    sts = "\n".join(["AWS4-HMAC-SHA256", amz_date, scope, sha256(canon.encode())])
    key = signing_key(amz_date[:8], region)
    sig = hmac.new(key, sts.encode(), hashlib.sha256).hexdigest()
    return {"canonical_request": canon, "string_to_sign": sts, "signing_key": key.hex(), "signature": sig,
            "authorization": f"AWS4-HMAC-SHA256 Credential={ACCESS}/{scope}, SignedHeaders={';'.join(names)}, Signature={sig}"}


def presign(method, host, path, expires, amz_date, region):
    scope = f"{amz_date[:8]}/{region}/s3/aws4_request"
    q = {"X-Amz-Algorithm": "AWS4-HMAC-SHA256", "X-Amz-Credential": f"{ACCESS}/{scope}", "X-Amz-Date": amz_date,
         "X-Amz-Expires": str(expires), "X-Amz-SignedHeaders": "host"}
    raw_q = "&".join(f"{enc(k)}={enc(v)}" for k, v in q.items())
    canon = "\n".join([method, canonical_uri(path), canonical_query(raw_q), f"host:{host}\n", "host", "UNSIGNED-PAYLOAD"])
    sts = "\n".join(["AWS4-HMAC-SHA256", amz_date, scope, sha256(canon.encode())])
    sig = hmac.new(signing_key(amz_date[:8], region), sts.encode(), hashlib.sha256).hexdigest()
    return {"canonical_request": canon, "string_to_sign": sts, "signature": sig,
            "url": f"http://{host}{path}?{raw_q}&X-Amz-Signature={sig}"}


T0 = "20260102T030405Z"
H = "zeros3.test:9000"
EMPTY = sha256(b"")
UTF = lambda s: s.encode()


def hdr(payload, extra=None, host=H, date=T0):
    h = {"Host": host, "x-amz-content-sha256": sha256(payload), "x-amz-date": date}
    h.update(extra or {})
    return h


# (name, method, raw_uri, payload, extra headers, region, host, date)
CASES = [
    ("ordinary path, empty payload", "GET", "/bucket/key.txt", b"", {}),
    ("space in key, non-empty payload", "PUT", "/bucket/a%20b.txt", b"hello", {}),
    ("plus in key", "PUT", "/bucket/a+b.txt", b"plus", {}),
    ("percent in key", "GET", "/bucket/100%25.txt", b"", {}),
    ("encoded slash in key", "GET", "/bucket/dir%2Ffile.txt", b"", {}),
    ("repeated slashes in key", "GET", "/bucket//a///b", b"", {}),
    ("unicode key", "GET", "/bucket/%C3%BC%E6%97%A5.txt", b"", {}),
    ("trailing slash key", "PUT", "/bucket/dir/", b"", {}),
    ("unsorted query, encoded values", "GET", "/bucket?prefix=p%2Fq%20r&list-type=2&max-keys=3&delimiter=%2F", b"", {}),
    ("valueless and repeated query names", "GET", "/bucket?uploads&b=2&a=1&a=0", b"", {}),
    ("metadata headers, whitespace collapsing", "PUT", "/bucket/meta.bin", b"\x00\x01\x02payload",
     {"Content-Type": "application/x-test", "x-amz-meta-origin": "  Mojo   Client  ", "x-amz-meta-n": "42"}),
    ("non-default region", "GET", "/bucket/key.txt", b"", {}),
]


def sigv4_vectors():
    out = []
    for name, method, uri, payload, extra in CASES:
        region = "eu-west-2" if name == "non-default region" else "us-east-1"
        h = hdr(payload, extra)
        signed = sorted(k.lower() for k in h)
        v = {"name": name, "method": method, "raw_uri": uri, "headers": h, "payload_utf8_hex": payload.hex(),
             "payload_sha256": sha256(payload), "region": region, "amz_date": T0, "signed_headers": signed}
        v.update(sign(method, uri, h, sha256(payload), T0, region, signed))
        out.append(v)
    # AWS-published worked examples (docs: "Signature Calculations for the Authorization Header").
    aws_host, d = "examplebucket.s3.amazonaws.com", "20130524T000000Z"
    ex = [
        ("AWS example: GET Object with Range", "GET", "/test.txt", b"", {"Range": "bytes=0-9"},
         "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"),
        ("AWS example: PUT Object", "PUT", "/test$file.text", b"Welcome to Amazon S3.",
         {"Date": "Fri, 24 May 2013 00:00:00 GMT", "x-amz-storage-class": "REDUCED_REDUNDANCY"},
         "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"),
        ("AWS example: GET Bucket Lifecycle", "GET", "/?lifecycle", b"", {},
         "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543"),
        ("AWS example: GET Bucket (List Objects)", "GET", "/?max-keys=2&prefix=J", b"", {},
         "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"),
    ]
    for name, method, uri, payload, extra, published in ex:
        h = hdr(payload, extra, aws_host, d)
        signed = sorted(k.lower() for k in h)
        v = {"name": name, "method": method, "raw_uri": uri, "headers": h, "payload_utf8_hex": payload.hex(),
             "payload_sha256": sha256(payload), "region": "us-east-1", "amz_date": d, "signed_headers": signed,
             "aws_published_signature": published}
        v.update(sign(method, uri, h, sha256(payload), d, "us-east-1", signed))
        out.append(v)
    return {"access_key": ACCESS, "secret_key": SECRET, "service": "s3", "vectors": out}


def presign_vectors():
    out = []
    cases = [
        ("AWS example: presigned GET", "GET", "examplebucket.s3.amazonaws.com", "/test.txt", 86400, "20130524T000000Z", "us-east-1"),
        ("GET, path style", "GET", H, "/bucket/key.txt", 900, T0, "us-east-1"),
        ("PUT, path style", "PUT", H, "/bucket/upload.bin", 3600, T0, "us-east-1"),
        ("GET, encoded key", "GET", H, "/bucket/dir/a%20b%2Bc%25d.txt", 900, T0, "us-east-1"),
        ("GET, unicode key", "GET", H, "/bucket/%C3%BC%E6%97%A5.txt", 900, T0, "us-east-1"),
        ("GET, minimum expiry", "GET", H, "/bucket/key.txt", 1, T0, "us-east-1"),
        ("GET, maximum expiry", "GET", H, "/bucket/key.txt", 604800, T0, "eu-west-2"),
    ]
    for name, method, host, path, expires, date, region in cases:
        v = {"name": name, "method": method, "host": host, "raw_path": path, "expires": expires, "amz_date": date, "region": region}
        v.update(presign(method, host, path, expires, date, region))
        if name.startswith("AWS example"):
            v["aws_published_signature"] = "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
        out.append(v)
    return {"access_key": ACCESS, "secret_key": SECRET, "vectors": out}


def main():
    files = {"sigv4.json": sigv4_vectors(), "presign.json": presign_vectors()}
    for doc in files.values():
        for v in doc["vectors"]:
            if "aws_published_signature" in v and v["aws_published_signature"] != v["signature"]:
                sys.exit(f"reference implementation disagrees with AWS's published signature for {v['name']}")
    for name, doc in files.items():
        text = json.dumps(doc, indent=1, ensure_ascii=True) + "\n"
        path = os.path.join(HERE, name)
        if "--check" in sys.argv:
            if open(path).read() != text:
                sys.exit(f"{name}: committed vectors differ from the reference implementation")
        else:
            open(path, "w").write(text)
    print("ok" if "--check" in sys.argv else "written")


if __name__ == "__main__":
    main()
