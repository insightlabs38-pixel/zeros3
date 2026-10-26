package prodshadow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/gitz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// Row is one scenario outcome. OK is the verdict against the invariant the row
// names; Auth is the authoritative-path result, Shadow what the shadow layer did.
type Row struct {
	Scenario, Auth, Shadow, After string
	OK                            bool
}

func (r Row) String() string {
	v := "ok"
	if !r.OK {
		v = "FAIL"
	}
	return fmt.Sprintf("%-4s %-34s auth=%s | shadow=%s | %s", v, r.Scenario, r.Auth, r.Shadow, r.After)
}

// authState checks GET and range GET (online) and, after stopping the store, the
// production `verify -deep` (the unmodified binary). It restarts nothing.
func authState(l *Lab, key string, want []byte) (string, bool) {
	if err := l.CheckGET(key, want); err != nil {
		return "GET FAILS: " + err.Error(), false
	}
	return "GET+range exact", true
}

func prodVerify(l *Lab) (string, bool) {
	m, ok, err := l.Store.Verify(true, "")
	if err != nil || !ok {
		return fmt.Sprintf("verify -deep FAILS (%v %v)", m["issues"], err), false
	}
	return fmt.Sprintf("verify -deep ok (unreachable chunks reported: %v)", m["unreachable_chunks"]), true
}

func restart(l *Lab, kill bool) error {
	if kill {
		l.Kill()
	} else if err := l.Stop(); err != nil {
		return err
	}
	return l.Start()
}

func classify(err error) string {
	switch {
	case err == nil:
		return "exact"
	case errors.Is(err, repz3.ErrMissing):
		return "fails cleanly (missing)"
	case errors.Is(err, repz3.ErrMismatch):
		return "fails cleanly (mismatch)"
	case errors.Is(err, ErrDescriptor):
		return "rejected (bad root)"
	}
	return "fails cleanly (" + firstLine(err.Error()) + ")"
}

func firstLine(s string) string {
	if len(s) > 60 {
		s = s[:60]
	}
	return strings.ReplaceAll(s, "\n", " ")
}

// RunFailureMatrix executes the seven shadow-failure cases of Z3-22. Each case
// starts a fresh production store, ends with the ordinary GET/range GET and the
// production deep verify, and never lets the shadow outcome influence them.
func RunFailureMatrix(b *Binaries, work string, sqlitePages int) ([]Row, error) {
	ch := kinds.SQLiteChain(sqlitePages)
	want := ch.Stages[0].Data
	var rows []Row
	finish := func(f *Fixture, name, shadow string, shadowOK bool) error {
		auth, ok := authState(f.L, f.Key, want)
		if err := f.L.Stop(); err != nil {
			return err
		}
		v, vok := prodVerify(f.L)
		rows = append(rows, Row{name, auth, shadow, v, ok && vok && shadowOK})
		return nil
	}
	newFx := func(name string) (*Fixture, error) {
		return NewFixture(b, filepath.Join(work, name), ch, Options{})
	}

	// 1. authoritative commit, no shadow
	f, err := newFx("c1")
	if err != nil {
		return nil, err
	}
	if _, err := f.Put(0, false, Hooks{}); err != nil {
		return nil, err
	}
	if err := restart(f.L, false); err != nil {
		return nil, err
	}
	roots, _ := f.L.Sh.Adopt()
	if err := finish(f, "1 commit, no shadow", fmt.Sprintf("absent (%d roots)", len(roots)), len(roots) == 0); err != nil {
		return nil, err
	}

	// 2. partial shadow staging, process killed before any root
	f, err = newFx("c2")
	if err != nil {
		return nil, err
	}
	st, err := f.Put(0, true, Hooks{AbortAfter: 40})
	if !errors.Is(err, ErrAbort) {
		return nil, fmt.Errorf("case 2: expected injected abort, got %v", err)
	}
	if err := restart(f.L, true); err != nil { // SIGKILL
		return nil, err
	}
	roots, bad := f.L.Sh.Adopt()
	orphans := 0
	for _, o := range st.Objs {
		if _, err := f.L.Br.Stat(o); err == nil {
			orphans++
		}
	}
	if err := finish(f, "2 partial components, killed", fmt.Sprintf("unusable (%d roots, %d bad); %d orphan objects await shadow GC", len(roots), len(bad), orphans), len(roots) == 0); err != nil {
		return nil, err
	}

	// 3. components complete and verified, root unpublished
	f, err = newFx("c3")
	if err != nil {
		return nil, err
	}
	_, err = f.Put(0, true, Hooks{BeforePublish: func() error { return ErrAbort }})
	if !errors.Is(err, ErrAbort) {
		return nil, fmt.Errorf("case 3: %v", err)
	}
	if err := restart(f.L, true); err != nil {
		return nil, err
	}
	roots, _ = f.L.Sh.Adopt()
	if err := finish(f, "3 complete, root unpublished", fmt.Sprintf("not visible (%d roots) although all bytes exist", len(roots)), len(roots) == 0); err != nil {
		return nil, err
	}

	// 4. published root, then a shadow-only dependency is deleted / corrupted / substituted
	for _, mode := range []string{"deleted", "corrupted", "substituted"} {
		f, err = newFx("c4" + mode)
		if err != nil {
			return nil, err
		}
		if _, err := f.Put(0, true, Hooks{}); err != nil {
			return nil, err
		}
		live, err := f.L.LiveFile(filepath.Join(work, "c4live"+mode))
		if err != nil {
			return nil, err
		}
		only, err := f.ShadowOnlyChunks(live)
		if err != nil || len(only) < 2 {
			return nil, fmt.Errorf("case 4: need shadow-only chunks: %d %v", len(only), err)
		}
		if err := f.L.Stop(); err != nil {
			return nil, err
		}
		p := ChunkPath(f.L.Store.Dir, only[0])
		switch mode {
		case "deleted":
			err = os.Remove(p)
		case "corrupted":
			var d []byte
			if d, err = os.ReadFile(p); err == nil {
				d[len(d)/2] ^= 0x40
				err = os.WriteFile(p, d, 0o644)
			}
		case "substituted": // another shadow chunk's bytes (same file size class) under this name
			var d []byte
			if d, err = os.ReadFile(ChunkPath(f.L.Store.Dir, only[1])); err == nil {
				err = os.WriteFile(p, d, 0o644)
			}
		}
		if err != nil {
			return nil, err
		}
		if err := f.L.Start(); err != nil {
			return nil, err
		}
		f.L.Sh.Adopt()
		cerr := f.L.CheckShadow(f.Reps[0], want)
		if err := finish(f, "4 root ok, dependency "+mode, classify(cerr), cerr != nil); err != nil {
			return nil, err
		}
	}

	// 5. corrupt root / descriptor files
	f, err = newFx("c5")
	if err != nil {
		return nil, err
	}
	if _, err := f.Put(0, true, Hooks{}); err != nil {
		return nil, err
	}
	if err := f.L.Stop(); err != nil {
		return nil, err
	}
	rootDir := f.L.Meta.rootsDir()
	ents, _ := os.ReadDir(rootDir)
	var rootFile string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".root") {
			rootFile = filepath.Join(rootDir, e.Name())
		}
	}
	orig, _ := os.ReadFile(rootFile)
	var verdicts []string
	allBad := true
	for _, mut := range []struct {
		name string
		fn   func([]byte) []byte
	}{
		{"bit flip", func(b []byte) []byte { b = append([]byte(nil), b...); b[len(b)/2] ^= 1; return b }},
		{"truncated", func(b []byte) []byte { return b[:len(b)/2] }},
		{"trailing byte", func(b []byte) []byte { return append(append([]byte(nil), b...), 0) }},
		{"empty", func(b []byte) []byte { return nil }},
	} {
		os.WriteFile(rootFile, mut.fn(orig), 0o644)
		roots, _, bad := f.L.Meta.Load()
		if len(roots) != 0 || len(bad) != 1 {
			allBad = false
		}
		verdicts = append(verdicts, mut.name)
	}
	os.WriteFile(rootFile, orig, 0o644)
	if err := f.L.Start(); err != nil {
		return nil, err
	}
	if err := finish(f, "5 corrupt root", "all rejected: "+strings.Join(verdicts, ", "), allBad); err != nil {
		return nil, err
	}

	// 7. interruption during metadata publication
	for _, point := range []string{"tmp-written", "renamed"} {
		f, err = newFx("c7" + point)
		if err != nil {
			return nil, err
		}
		_, err = f.Put(0, true, Hooks{MetaHook: func(p string) error {
			if p == point {
				return ErrAbort
			}
			return nil
		}})
		if !errors.Is(err, ErrAbort) {
			return nil, fmt.Errorf("case 7: %v", err)
		}
		if err := restart(f.L, true); err != nil {
			return nil, err
		}
		roots, bad := f.L.Sh.Adopt()
		var shadow string
		okShadow := false
		switch point {
		case "tmp-written":
			shadow = fmt.Sprintf("crash before rename: no root (%d), temp file ignored", len(roots))
			okShadow = len(roots) == 0 && len(bad) == 0
		default:
			var e error
			if len(roots) == 1 {
				e = f.L.CheckShadow(roots[0].Rep, want)
			} else {
				e = errors.New("no root")
			}
			shadow = fmt.Sprintf("crash after rename: root valid (%d), shadow %s", len(roots), classify(e))
			okShadow = len(roots) == 1 && e == nil
		}
		if err := finish(f, "7 metadata publish @"+point, shadow, okShadow); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// RunCatalogLoss is case 6: the native accelerator is derived state. It is built
// from live shadow roots, discarded, restarted, and rebuilt from only the
// persisted roots and the production-backed index sections; the two must agree.
func RunCatalogLoss(b *Binaries, work string) ([]Row, error) {
	ch, err := kinds.GitFixtureChain(gitz3.SHA1)
	if err != nil {
		return nil, err
	}
	f, err := NewFixture(b, filepath.Join(work, "c6"), ch, Options{})
	if err != nil {
		return nil, err
	}
	defer f.L.Stop()
	for i := range ch.Stages {
		if _, err := f.Put(i, true, Hooks{}); err != nil {
			return nil, err
		}
	}
	cat1 := kinds.NewCatalog(f.L.Sh.Src)
	for _, rep := range f.Reps {
		if err := cat1.Ingest(rep); err != nil {
			return nil, err
		}
	}
	m1, entries1, secs1 := cat1.Mappings(), cat1.Entries(), cat1.NSections()
	if err := restart(f.L, false); err != nil {
		return nil, err
	}
	roots, bad := f.L.Sh.Adopt()
	var reps []repz3.SHA
	for _, r := range roots {
		reps = append(reps, r.Rep)
	}
	cat2, err := repz3.Rebuild(f.L.Sh.Src, kinds.Domain, reps)
	if err != nil {
		return nil, err
	}
	same := len(bad) == 0 && len(roots) == len(ch.Stages) && entries1 == cat2.Entries() && secs1 == cat2.NSections() && fmt.Sprint(sortedMap(m1)) == fmt.Sprint(sortedMap(cat2.Mappings()))
	auth, ok := authState(f.L, f.Key, ch.Stages[len(ch.Stages)-1].Data)
	return []Row{{"6 catalog lost, rebuilt from roots", auth,
		fmt.Sprintf("%d entries / %d sections rebuilt identically=%v", cat2.Entries(), cat2.NSections(), same), "catalog is derived, not persisted", same && ok}}, nil
}

func sortedMap(m map[string][]repz3.SHA) []string {
	var out []string
	for k, v := range m {
		out = append(out, fmt.Sprintf("%s=%x", k, v))
	}
	sortStrings(out)
	return out
}
