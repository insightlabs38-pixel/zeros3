package dexz3

import (
	"bytes"
	"encoding/binary"
	"errors"
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
