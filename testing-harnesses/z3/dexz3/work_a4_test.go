package dexz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestA4SharedPrototypeExpansionWorkBounded(t *testing.T) {
	const protos = 2048
	const params = 65536
	off := protos * 12
	data := make([]byte, off+4+params*2)
	for i := 0; i < protos; i++ {
		binary.LittleEndian.PutUint32(data[i*12+8:], uint32(off))
	}
	binary.LittleEndian.PutUint32(data[off:], params)
	f := &File{Spans: []Span{{Type: TypeProtoID, Off: 0, Count: protos}}}
	source := &walkSource{r: bytes.NewReader(data), size: int64(len(data)), lo: -1}
	_, e := identProtosAt(source, f, []Identity{{1}})
	if !errors.Is(e, ErrLimit) || source.identityWork > source.size*16+(64<<20) {
		t.Fatal("shared list expansion not bounded", source.identityWork, e)
	}
}

type prototypeBrokenParameters struct {
	data []byte
	from int64
}

func (s prototypeBrokenParameters) ReadAt(p []byte, off int64) (int, error) {
	if off >= s.from {
		return 0, io.ErrClosedPipe
	}
	return bytes.NewReader(s.data).ReadAt(p, off)
}
func TestA4PrototypeParameterSourceFailure(t *testing.T) {
	const list = 65536
	data := make([]byte, list+6)
	binary.LittleEndian.PutUint32(data[8:], list)
	binary.LittleEndian.PutUint32(data[list:], 1)
	f := &File{Spans: []Span{{Type: TypeProtoID, Off: 0, Count: 1}}}
	source := &walkSource{r: prototypeBrokenParameters{data, list}, size: int64(len(data)), lo: -1}
	if _, e := identProtosAt(source, f, []Identity{{1}}); !errors.Is(e, io.ErrClosedPipe) {
		t.Fatal("parameter-reader error lost", e)
	}
}
