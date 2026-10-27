package kinds

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"zeros3-z3/parquetz3"
	"zeros3-z3/repz3"
)

func pqFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "parquetz3", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The kind rebuilds real producer files through the serialized representation only, every
// object the reconstruction reads is kept alive by the contract's reachability, and the
// components two layouts of the same table share are single objects with two immutable
// references that outlive the first root and disappear with the last.
func TestParquetKindIsExactReachableAndShared(t *testing.T) {
	for _, name := range []string{"pa_snappy_v1.parquet", "pa_snappy_v2.parquet", "pa_zstd_v1.parquet", "pa_none_plain.parquet", "pa_nested.parquet", "dd_snappy.parquet", "dd_zstd.parquet"} {
		a := pqFile(t, name)
		b, _, _, err := EncodeParquet(bytes.NewReader(a), int64(len(a)), parquetz3.Options{Target: 4 << 10})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := Prove(nil, b, a); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	t1, t2 := pqFile(t, "pa_snappy_v1.parquet"), pqFile(t, "pa_rg500.parquet") // same table, other row groups
	b1, _, _, err := EncodeParquet(bytes.NewReader(t1), int64(len(t1)), parquetz3.Options{})
	if err != nil {
		t.Fatal(err)
	}
	b2, _, _, err := EncodeParquet(bytes.NewReader(t2), int64(len(t2)), parquetz3.Options{})
	if err != nil {
		t.Fatal(err)
	}
	st := repz3.NewMemStore()
	id1, _, _ := b1.Persist(st)
	id2, added, _ := b2.Persist(st)
	if len(added) >= len(b2.Objects)+1 {
		t.Fatalf("no component of the second layout was already stored (%d new of %d)", len(added), len(b2.Objects)+1)
	}
	env := NewEnv(st)
	r1, err := env.Reachable(id1)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := env.Reachable(id2)
	if err != nil {
		t.Fatal(err)
	}
	shared := 0
	for id := range r2.Role {
		if _, ok := r1.Role[id]; ok {
			shared++
		}
	}
	if shared == 0 {
		t.Fatal("the two representations share no object")
	}
	for _, id := range st.IDs() {
		if _, ok := r2.Role[id]; !ok {
			st.Delete(id)
		}
	}
	var out bytes.Buffer
	if _, _, err := NewEnv(st).Reconstruct(id2, &out); err != nil || !bytes.Equal(out.Bytes(), t2) {
		t.Fatalf("second layout after the first one was collected: %v", err)
	}
	if _, _, err := NewEnv(st).Reconstruct(id1, new(bytes.Buffer)); err == nil {
		t.Fatal("the collected layout still reconstructs")
	}
	for _, id := range st.IDs() {
		st.Delete(id)
	}
	if st.Len() != 0 {
		t.Fatal("store not empty")
	}
}

func TestParquetKindReadsAndFallback(t *testing.T) {
	a := pqFile(t, "dd_snappy.parquet")
	b, _, _, err := EncodeParquet(bytes.NewReader(a), int64(len(a)), parquetz3.Options{})
	if err != nil {
		t.Fatal(err)
	}
	st := repz3.NewMemStore()
	id, _, _ := b.Persist(st)
	c, err := NewEnv(st).Open(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, rg := range [][2]int64{{0, 100}, {5000, 4096}, {int64(len(a)) / 2, 9000}, {int64(len(a)) - 50, 50}} {
		got, stats, err := ReadParquetRange(c, rg[0], rg[1])
		if err != nil || !bytes.Equal(got, a[rg[0]:rg[0]+rg[1]]) {
			t.Fatalf("range %v: %v", rg, err)
		}
		if rg[0] > 0 && rg[0]+rg[1] < int64(len(a))-2000 && stats.Components == 0 { // the footer is inline in the recipe
			t.Fatalf("range %v fetched nothing", rg)
		}
	}
	// a file the codec registry cannot express never becomes a representation
	g := pqFile(t, "pa_gzip.parquet")
	if _, _, _, err := EncodeParquet(bytes.NewReader(g), int64(len(g)), parquetz3.Options{}); err == nil || parquetz3.Supported(err) {
		t.Fatalf("gzip file: %v", err)
	}
}
