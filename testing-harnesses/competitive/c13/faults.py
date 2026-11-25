"""Check scope, dictionary dependencies and atomic cohort publication."""
import argparse
import json
from pathlib import Path
import shutil
import tempfile

from benchmark import check, fixtures
from cohort import Archive, dict_identity, index_bytes, publish, write_all


def refuse(fn, failure, name):
    try:
        fn()
    except failure:
        return name
    raise ValueError(f"failed to refuse {name}")


def open_archive(root, scope):
    with Archive(root, scope):
        pass


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("work", "corpus", "manifest"):
        p.add_argument("--" + name, type=Path, required=True)
    args = p.parse_args()
    args.work.mkdir(parents=True, exist_ok=True)
    scope = "main"
    test = fixtures(args.corpus, args.manifest, scope)
    rows = []
    with tempfile.TemporaryDirectory(dir=args.work) as temp:
        tmp = Path(temp)
        root = tmp / "archive"
        publish(root, scope, "prefix", test)
        with Archive(root, scope) as original:
            check(original, test)
            ident, entry = next((key, row) for key, row in original.index["dicts"].items() if row[5] > 2)
            family, pack, offset, _, _, _ = entry
            raw = original.dictionaries[ident]
            if dict_identity(scope, family, raw) == dict_identity("different", family, raw):
                raise ValueError("dictionary identity leaked between scopes")
            rows.append(refuse(lambda: open_archive(root, "different"), ValueError, "wrong-scope"))
            corrupt = tmp / "corrupt-dictionary"
            shutil.copytree(root, corrupt)
            with (corrupt / pack).open("r+b") as out:
                out.seek(offset)
                out.write(bytes([raw[0] ^ 1]))
            rows.append(refuse(lambda: open_archive(corrupt, scope), ValueError, "corrupt-dictionary"))
            missing = tmp / "missing-dictionary"
            shutil.copytree(root, missing)
            index = json.loads(json.dumps(original.index))
            del index["dicts"][ident]
            write_all(missing / "INDEX", index_bytes(index))
            rows.append(refuse(lambda: open_archive(missing, scope), ValueError, "missing-dictionary-reference"))
            (missing / pack).unlink()
            write_all(missing / "INDEX", index_bytes(original.index))
            rows.append(refuse(lambda: open_archive(missing, scope), FileNotFoundError, "missing-dictionary-pack"))
            payload = b"audited revision" * 43
            addition = {"config/update/0000.bin": payload}
            key = "config/eval/0000.bin"
            rows.append(refuse(lambda: publish(root, scope, "prefix", addition, original,
                                               delete=(key,), cut_before_index=True),
                               InterruptedError, "cut-before-index"))
            with Archive(root, scope) as reopened:
                check(reopened, test)
            updated = {k: v for k, v in test.items() if k != key}
            updated.update(addition)
            publish(root, scope, "prefix", addition, original, delete=(key,))
            rows.append(refuse(lambda: publish(root, scope, "prefix", addition, original),
                               ValueError, "stale-generation"))
            with Archive(root, scope) as newer:
                check(newer, updated)
                if ident not in newer.dictionaries:
                    raise ValueError("one deletion removed a shared dictionary")
                publish(root, scope, "prefix", updated, newer, rebuild=True)
            if original.get(key) != test[key]:
                raise ValueError("old reader lost its leased generation")
            rows.append("held-reader-exact")
        with Archive(root, scope) as fresh:
            check(fresh, updated)
            rows.append("fresh-generation-exact")
            retired = {key for key, row in fresh.index["objects"].items()
                       if fresh.index["blocks"][row[0]][5] == ident}
            if not retired:
                raise ValueError("rebuilt proof lacks the shared dictionary")
            publish(root, scope, "prefix", {}, fresh, delete=retired)
        with Archive(root, scope) as final:
            check(final, {k: v for k, v in updated.items() if k not in retired})
            if ident in final.dictionaries:
                raise ValueError("unreferenced dictionary stayed in the visible closure")
            rows.append("last-parent-released")
    print(json.dumps({"checks": rows, "objects": len(test), "exact": True}))


if __name__ == "__main__":
    main()
