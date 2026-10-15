package ociz3

import (
	"bytes"
	"testing"

	"zeros3-z3/corpus"
	"zeros3-z3/fstate"
	"zeros3-z3/lab"
)

// OCI blobs fit the shared Z3-15F shape without new fields: converting a plan to a
// srev.Revision and back reproduces the accounting and the exact rebuild, Roots and
// Bases stay empty, and a single-chunk blob's registry digest is its component SHA.
func TestSrevShapeFitsOCI(t *testing.T) {
	b, root := tinyImage(t, nil)
	img, err := Walk(b.Blobs, root, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	nat, err := Natives(img)
	if err != nil {
		t.Fatal(err)
	}
	if nat.Blobs != len(img.Nodes) || nat.SingleChunk != nat.Blobs || nat.Multi != 0 {
		t.Errorf("tiny blobs are single chunks: %+v", nat)
	}
	for _, n := range img.Nodes {
		pl, err := fstate.FallbackCDC(fstate.File{Name: n.Digest, Data: n.Data})
		if err != nil {
			t.Fatal(err)
		}
		rev := RevisionOf(pl, n.Digest)
		if len(rev.Roots) != 0 || len(rev.Bases) != 0 {
			t.Fatal("Roots/Bases used")
		}
		if len(rev.Members) != 1 || len(rev.Members[0].C.Native) != 32 || string(rev.Members[0].C.Native) != string(rev.Members[0].C.SHA[:]) {
			t.Errorf("native digest of a single-chunk blob must be its component SHA")
		}
		a1, a2 := lab.NewAcct(96, true), lab.NewAcct(96, true)
		pl2 := lab.PlanFromRevision(rev)
		x1, err := a1.Add(pl)
		if err != nil {
			t.Fatal(err)
		}
		x2, err := a2.Add(pl2)
		if err != nil {
			t.Fatal(err)
		}
		if lab.TableCost(x1) != lab.TableCost(x2) || x1.NewBytes != x2.NewBytes {
			t.Errorf("cost %d vs %d", lab.TableCost(x1), lab.TableCost(x2))
		}
		var out bytes.Buffer
		if err := pl2.Rebuild(&out, a2.Get); err != nil || !bytes.Equal(out.Bytes(), n.Data) {
			t.Errorf("rebuild: %v", err)
		}
	}
	// a multi-chunk blob needs a digest -> component-list entry
	big := corpus.NewRNG(1, "srev-big").Bytes(400 << 10)
	mb, err := ImageSpec{OS: "linux", Arch: "amd64", Layers: []LayerSpec{{Tar: big}}}.Build()
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := RootIndex([]Descriptor{mb.Desc}, nil, MTOCIIndex)
	img2, err := Walk(mb.Blobs, r2, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	n2, err := Natives(img2)
	if err != nil {
		t.Fatal(err)
	}
	if n2.Multi != 1 || n2.MultiChunks < 2 || n2.MappingBytes <= 0 || n2.SingleChunk != 2 {
		t.Errorf("%+v", n2)
	}
}
