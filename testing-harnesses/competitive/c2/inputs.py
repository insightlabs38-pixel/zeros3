"""Build bounded B8–B10 replay subsets and a held-out wheel history."""
import argparse
from hashlib import sha256
import json
from pathlib import Path, PurePosixPath
import tarfile
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[2] / "next"


def hashes(milestone):
    return {name: digest for digest, name in
            (line.split() for line in (ROOT / milestone / "corpus.sha256").read_text().splitlines())}


def pypi(package, version, kind):
    with urlopen(f"https://pypi.org/pypi/{package}/{version}/json", timeout=20) as response:
        release = json.load(response)
    return next((file["url"], file["digests"]["sha256"]) for file in release["urls"]
                if file["packagetype"] == kind)


def inputs():
    old8, old9, old10 = map(hashes, ("b8", "b9", "b10"))
    for i, v in enumerate(("5.6.2", "5.6.3", "5.7.2")):
        name = f"typescript-{v}.tgz"
        yield "typescript", i, name, f"https://registry.npmjs.org/typescript/-/{name}", old8["ts/" + name]
    for i, v in enumerate(("5.1.1", "5.1.2", "5.1.3")):
        name = f"Django-{v}.tar.gz"
        url, _ = pypi("Django", v, "sdist")
        yield "django", i, name, url, old8["django/" + name]
    for i, v in enumerate(("1.66.0", "1.66.1", "1.67.0")):
        name = f"rubocop-{v}.gem"
        yield "rubocop", i, name, f"https://rubygems.org/downloads/{name}", old10["gems/" + name]
    for i, v in enumerate(("9.5", "9.6", "9.7")):
        name = f"coreutils-{v}.tar.xz"
        yield "coreutils-files", i, name, f"https://ftp.gnu.org/gnu/coreutils/{name}", old8["src/" + name]
    for i, v in enumerate(("1.22.0", "1.22.6", "1.23.0")):
        name = f"go{v}.src.tar.gz"
        yield "go-files", i, name, f"https://go.dev/dl/{name}", old9["go/" + name]
    for i, v in enumerate(("2.2.1", "2.2.2", "2.2.3")):
        url, digest = pypi("urllib3", v, "bdist_wheel")
        yield "urllib3", i, url.rsplit("/", 1)[1], url, digest


def extract(archive, dest, family):
    with tarfile.open(archive) as tar:
        candidates = []
        for m in tar:
            parts = PurePosixPath(m.name).parts[1:]
            if (m.isfile() and len(parts) > 1 and parts[0] == "src"
                    and all(x not in ("..", "/") for x in parts)
                    and m.name.endswith(".go" if family == "go-files" else ".c")
                    and 4096 <= m.size <= 256 * 1024):
                candidates.append((parts, m))
        for parts, m in sorted(candidates, key=lambda item: item[0])[:64]:
            path = dest.joinpath(*parts)
            path.parent.mkdir(parents=True, exist_ok=True)
            with tar.extractfile(m) as content:
                path.write_bytes(content.read())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("corpus", type=Path)
    parser.add_argument("downloads", type=Path)
    parser.add_argument("manifest", type=Path)
    args = parser.parse_args()
    args.downloads.mkdir(parents=True, exist_ok=True)
    records = []
    for family, rev, name, url, expected in inputs():
        archive = args.downloads / name
        if not archive.exists():
            with urlopen(url, timeout=45) as response:
                data = response.read(40 * 1024 * 1024 + 1)
            if len(data) > 40 * 1024 * 1024 or sha256(data).hexdigest() != expected:
                raise ValueError(f"invalid archived release: {name}")
            archive.write_bytes(data)
        if sha256(archive.read_bytes()).hexdigest() != expected:
            raise ValueError(f"checksum mismatch: {name}")
        dest = args.corpus / family / str(rev)
        dest.mkdir(parents=True, exist_ok=True)
        if family.endswith("-files"):
            extract(archive, dest, family)
        else:
            (dest / name).write_bytes(archive.read_bytes())
        files = [{"path": p.relative_to(args.corpus).as_posix(), "sha256": sha256(p.read_bytes()).hexdigest(),
                  "bytes": p.stat().st_size} for p in sorted(dest.rglob("*")) if p.is_file()]
        records.append({"source": url, "sha256": expected, "files": files})
        args.manifest.write_text(json.dumps(records, indent=2) + "\n")


if __name__ == "__main__":
    main()
