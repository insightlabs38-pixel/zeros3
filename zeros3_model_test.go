package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"testing"
)

type modelWorkSource struct {
	*repMem
	reads atomic.Int64
}

func (s *modelWorkSource) ReadAt(id [32]byte, p []byte, off int64) (int, error) {
	if s.reads.Add(1) > 600000 {
		return 0, fmt.Errorf("test safety ceiling before aggregate decoder limit")
	}
	return s.repMem.ReadAt(id, p, off)
}
func TestModelAggregateZeroReplayBound(t *testing.T) {
	mem := newRepMem()
	empty := mem.put(nil)
	id := mem.putRep(&repObject{Kind: repKindModel, Members: []repMember{{Role: repStored, SHA: empty}}, Recipe: repMDBuildModelRecipe([]repMDModelOp{{Tag: repMDopCopy}})})
	const fanout = 1100
	for level := 0; level < 2; level++ {
		ops := make([]repMDModelOp, fanout)
		for i := range ops {
			ops[i] = repMDModelOp{Tag: repMDopCopy}
		}
		id = mem.putRep(&repObject{Kind: repKindModel, Members: []repMember{{Role: repDerived, SHA: sha256.Sum256(nil), Child: id}}, Recipe: repMDBuildModelRecipe(ops)})
	}
	source := &modelWorkSource{repMem: mem}
	env := &repEnv{Src: source, Limits: repProdLimits}
	_, _, err := env.reconstruct(id, io.Discard)
	if !errors.Is(err, errRepLimit) {
		t.Fatalf("aggregate replay must fail with bounded work, reads=%d: %v", source.reads.Load(), err)
	}
	if source.reads.Load() >= 600000 {
		t.Fatal("work guard fired too late")
	}
	t.Logf("bounded repeated zero-output DAG after %d source reads", source.reads.Load())
}

func TestModelAggregateByteReservationOverflow(t *testing.T) {
	w := newRepDecodeWork(0)
	if err := w.enter(&repObject{Size: 1<<63 - 1}); !errors.Is(err, errRepLimit) {
		t.Fatalf("oversized child charge: %v", err)
	}
	w = newRepDecodeWork(1<<63 - 1)
	if err := w.enter(&repObject{Size: 1<<63 - 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.enter(&repObject{Size: 1}); !errors.Is(err, errRepLimit) {
		t.Fatalf("reservation overflow: %v", err)
	}
}

func TestModelRangeWorkChargesWindowNotWholeTensor(t *testing.T) {
	mem := newRepMem()
	const size = 16 << 20
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i)
	}
	stored := mem.put(data)
	child := mem.putRep(&repObject{Kind: repKindModel, Size: size, Members: []repMember{{Role: repStored, SHA: stored, Size: size}}, Recipe: repMDBuildModelRecipe([]repMDModelOp{{Tag: repMDopCopy}})})
	root := &repObject{Kind: repKindModel, Size: size, Members: []repMember{{Role: repDerived, SHA: sha256.Sum256(data), Size: size, Child: child}}, Recipe: repMDBuildModelRecipe([]repMDModelOp{{Tag: repMDopCopy}})}
	call := mem.env(repProdLimits).call([32]byte{}, root, 1, nil, nil)
	for off := int64(0); off < size; off += repModelWindow {
		got, err := repMDReadModelRange(call, off, repModelWindow)
		if err != nil {
			t.Fatal(err)
		}
		if sha256.Sum256(got) != sha256.Sum256(data[off:off+repModelWindow]) {
			t.Fatal("range mismatch")
		}
	}
}

func FuzzModelMetadata(f *testing.F) {
	f.Add(repZ4ModelSafetensors(f, []byte{1, 2, 3, 4}))
	gguf, err := os.ReadFile("testing-harnesses/z4/testdata/a2-model/model.gguf")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(gguf)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		repModelLayout(bytes.NewReader(data), int64(len(data)))
	})
}

func TestModelEmptyMembersStillVerifyDependencies(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprint(present), func(t *testing.T) {
			mem := newRepMem()
			id := sha256.Sum256([]byte("not the empty digest"))
			if present {
				mem.objs[id] = nil
			}
			root := mem.putRep(&repObject{Kind: repKindModel, Members: []repMember{{Role: repStored, SHA: id}}, Recipe: repMDBuildModelRecipe([]repMDModelOp{{Tag: repMDopCopy}})})
			_, _, err := mem.env(repProdLimits).reconstruct(root, io.Discard)
			want := errRepMissing
			if present {
				want = errRepMismatch
			}
			if !errors.Is(err, want) {
				t.Fatalf("empty member dependency: %v", err)
			}
		})
	}
}

func TestModelNestedPublicationRaisesFloor(t *testing.T) {
	l := newRepLab(t, repModeOff)
	data := repZ4ModelSafetensors(t, bytes.Repeat([]byte{1, 2, 3, 4}, 16384))
	l.put("model", data)
	entry, _ := l.manifest("model")
	if l.s.format.StoreFormatVersion != 1 {
		t.Fatal("ordinary off-mode PUT raised format")
	}
	b := l.s.newRepBuilder(int64(len(data)), nil, nil)
	defer b.close()
	model, err := b.encodeModel(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	child, err := b.finishRep(model)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	outer := &repObject{Kind: repKindConcat, Size: int64(len(data)), Members: []repMember{{Role: repDerived, SHA: sum, Size: int64(len(data)), Child: child}}}
	id, err := b.finishRep(outer)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.flush(); err != nil {
		t.Fatal(err)
	}
	if err = b.proof(id, int64(len(data)), sum); err != nil {
		t.Fatal(err)
	}
	root := &repRoot{ManifestUUID: entry.manifestUUID, ObjSHA: sum, ObjSize: int64(len(data)), Rep: id, Descs: b.descs}
	if err = l.s.publishRepRoot(root); err != nil {
		t.Fatal(err)
	}
	if l.s.format.StoreFormatVersion != 7 {
		t.Fatalf("nested Model floor=%d", l.s.format.StoreFormatVersion)
	}
	l.close()
	l.open()
	l.exact("nested Model publication restart")
}
