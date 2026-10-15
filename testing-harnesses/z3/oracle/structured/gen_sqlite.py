"""SQLite family: ~24 MiB main database file, 12 independent mutations of r0 (no WAL/journal files kept)."""
import os, shutil, sqlite3, sys
import numpy as np
from common import Family, rng

PS = 4096


def con(path):
    c = sqlite3.connect(path, isolation_level=None)
    c.execute("PRAGMA journal_mode=DELETE")
    c.execute("PRAGMA synchronous=OFF")
    return c


def body(r, n):
    # partly compressible row payload: random hex + a repeated tag
    return (r.bytes(n // 2).hex() + "|record-body-" * 8)[:n].encode()


def base(path):
    r = rng("sqlite-base")
    c = con(path)
    c.execute(f"PRAGMA page_size={PS}")
    c.execute("CREATE TABLE kv(id INTEGER PRIMARY KEY, name TEXT, score INTEGER, body BLOB)")
    c.execute("CREATE INDEX kv_name ON kv(name)")
    c.execute("CREATE TABLE blobs(id INTEGER PRIMARY KEY, data BLOB)")
    c.execute("BEGIN")
    names = r.randint(0, 2**31, size=60000)
    for i in range(60000):
        c.execute("INSERT INTO kv VALUES(?,?,?,?)", (i + 1, "user-%08x" % names[i], int(r.randint(0, 1000)), body(r, 200)))
    for i in range(8):
        c.execute("INSERT INTO blobs VALUES(?,?)", (i + 1, r.bytes(512 << 10)))
    c.execute("COMMIT")
    c.close()


def mutate(fam, name, mutation, fn, logical, note="", out="db.sqlite"):
    d = fam.rev(name)
    dst = os.path.join(d, out)
    shutil.copy(os.path.join(fam.dir, "r0", "db.sqlite"), dst)
    c = con(dst)
    fn(c, dst)
    c.close()
    fam.record(name, mutation, logical, note)


def main():
    fam = Family("sqlite", "kv24", {"sqlite": sqlite3.sqlite_version, "page_size": PS})
    d0 = fam.rev("r0")
    base(os.path.join(d0, "db.sqlite"))
    fam.record("r0", "base", 0)
    r = rng("sqlite-mut")

    def upd1(c, p): c.execute("UPDATE kv SET body=? WHERE id=30000", (body(r, 200),))
    mutate(fam, "m01-update-one-row", "one row changed (same length)", upd1, 200)

    def upd100(c, p):
        c.execute("BEGIN")
        for i in r.randint(1, 60001, size=100):
            c.execute("UPDATE kv SET body=? WHERE id=?", (body(r, 200), int(i)))
        c.execute("COMMIT")
    mutate(fam, "m02-update-100-sparse", "100 sparse rows changed", upd100, 100 * 200)

    def grow(c, p): c.execute("UPDATE kv SET body=? WHERE id=20000", (body(r, 900),))
    mutate(fam, "m03-update-grow", "one row grown 200->900 B", grow, 900)

    def app(c, p):
        c.execute("BEGIN")
        for i in range(600):
            c.execute("INSERT INTO kv VALUES(?,?,?,?)", (60001 + i, "user-%08x" % int(r.randint(0, 2**31)), 1, body(r, 200)))
        c.execute("COMMIT")
    mutate(fam, "m04-append-1pct", "append 600 rows (1%) with random index keys", app, 600 * 230)

    def dele(c, p): c.execute("DELETE FROM kv WHERE id % 10 = 3")
    mutate(fam, "m05-delete-10pct", "delete 10% rows, no VACUUM", dele, 0, "deleted rows are free space; logical change is the removal")

    def delv(c, p):
        c.execute("DELETE FROM kv WHERE id % 10 = 3"); c.execute("VACUUM")
    mutate(fam, "m06-delete-vacuum", "delete 10% rows then VACUUM", delv, 0)

    def ins(c, p):
        c.execute("BEGIN")
        for i in range(2000):
            c.execute("INSERT INTO kv VALUES(NULL,?,?,?)", ("user-%08x" % int(r.randint(0, 2**31)), 2, body(r, 200)))
        c.execute("COMMIT")
    mutate(fam, "m07-insert-random-2000", "2000 rows with random index keys (B-tree splits)", ins, 2000 * 230)

    def meta(c, p): c.execute("PRAGMA user_version=7")
    mutate(fam, "m08-metadata-only", "PRAGMA user_version (header only)", meta, 4)

    def vac(c, p): c.execute("VACUUM")
    mutate(fam, "m09-vacuum", "same logical contents, VACUUM rebuild", vac, 0)

    def ps8(c, p):
        c.execute("PRAGMA page_size=8192"); c.execute("VACUUM")
    mutate(fam, "m10-page-size-8k", "same contents, page_size 4096->8192", ps8, 0)

    def tiny(c, p):
        c.execute("BEGIN")
        for i in r.randint(1, 60001, size=5000):
            c.execute("UPDATE kv SET score=score+1 WHERE id=?", (int(i),))
        c.execute("COMMIT")
    mutate(fam, "m11-tiny-updates-5000", "5000 one-column updates", tiny, 5000 * 2)

    def blobu(c, p):
        c.execute("UPDATE blobs SET data=substr(data,1,262144)||?||substr(data,262161) WHERE id=3", (r.bytes(16),))
    mutate(fam, "m12-sparse-in-blob", "16 B edit inside a 512 KiB overflow blob", blobu, 16)
    def upd(n):
        def f(c, p):
            c.execute("BEGIN")
            for i in r.randint(1, 60001, size=n):
                c.execute("UPDATE kv SET body=? WHERE id=?", (body(r, 200), int(i)))
            c.execute("COMMIT")
        return f
    mutate(fam, "m13-update-1000", "1000 sparse rows changed", upd(1000), 1000 * 200)
    mutate(fam, "m14-update-10000", "10000 sparse rows changed", upd(10000), 10000 * 200)
    mutate(fam, "m15-update-30000", "30000 sparse rows changed", upd(30000), 30000 * 200)
    mutate(fam, "m16-update-10", "10 sparse rows changed", upd(10), 10 * 200)
    fam.close()
    history()


def history():
    """A 10-revision chain of an app-like database (messages + attachments), each revision derived from the previous one."""
    r = rng("sqlite-hist")
    fam = Family("sqlite", "hist10", {"sqlite": sqlite3.sqlite_version, "page_size": PS})
    d = fam.rev("r0"); p = os.path.join(d, "app.sqlite")
    c = con(p); c.execute(f"PRAGMA page_size={PS}")
    c.execute("CREATE TABLE msg(id INTEGER PRIMARY KEY, chan INTEGER, ts INTEGER, body TEXT)")
    c.execute("CREATE INDEX msg_chan ON msg(chan, ts)")
    c.execute("CREATE TABLE att(id INTEGER PRIMARY KEY, msg INTEGER, data BLOB)")
    c.execute("BEGIN")
    for i in range(30000): c.execute("INSERT INTO msg VALUES(?,?,?,?)", (i + 1, int(r.randint(0, 40)), 1700000000 + i * 7, body(r, 160).decode()))
    for i in range(120): c.execute("INSERT INTO att VALUES(?,?,?)", (i + 1, int(r.randint(1, 30000)), r.bytes(24000)))
    c.execute("COMMIT"); c.close()
    fam.record("r0", "base", 0)
    prev, nmsg, natt = "r0", 30000, 120
    for step in range(1, 10):
        name = "r%d" % step
        d = fam.rev(name); p = os.path.join(d, "app.sqlite")
        shutil.copy(os.path.join(fam.dir, prev, "app.sqlite"), p)
        c = con(p); c.execute("BEGIN")
        for i in range(300): nmsg += 1; c.execute("INSERT INTO msg VALUES(?,?,?,?)", (nmsg, int(r.randint(0, 40)), 1700000000 + nmsg * 7, body(r, 160).decode()))
        for i in r.randint(1, nmsg, size=200): c.execute("UPDATE msg SET body=? WHERE id=?", (body(r, 160).decode(), int(i)))
        c.execute("DELETE FROM msg WHERE id % 997 = ?", (step,))
        if step % 3 == 0:
            for i in range(2): natt += 1; c.execute("INSERT INTO att VALUES(?,?,?)", (natt, int(r.randint(1, nmsg)), r.bytes(30000)))
        c.execute("COMMIT")
        note = ""
        if step == 6: c.execute("VACUUM"); note = "VACUUM"
        if step == 9: c.execute("REINDEX"); note = "REINDEX"
        c.close()
        fam.record(name, "day %d%s" % (step, " + " + note if note else ""), 300 * 190 + 200 * 160, base=prev)
        prev = name
    fam.close()


main()
