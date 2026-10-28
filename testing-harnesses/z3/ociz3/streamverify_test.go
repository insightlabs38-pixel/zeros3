package ociz3

import (
	"bytes"
	"compress/gzip"
	"errors"
	"testing"
	"zeros3-z3/zstdz3"
)

func TestVerifyLayerStreamsExactIdentities(t *testing.T) {
	plain := bytes.Repeat([]byte("OCI exact layer identity\n"), 4096)
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(plain)
	w.Close()
	zst, err := zstdz3.Synth(plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, media string
		data        []byte
	}{{"tar", MTOCILayerTar, plain}, {"gzip", MTOCILayerGzip, gz.Bytes()}, {"zstd", MTOCILayerZstd, zst}, {"opaque", "application/x-unknown", gz.Bytes()}} {
		t.Run(tc.name, func(t *testing.T) {
			d := Descriptor{MediaType: tc.media, Digest: Sum256(tc.data), Size: int64(len(tc.data))}
			got, err := VerifyLayer(bytes.NewReader(tc.data), d, Sum256(plain), Limits{MaxBlob: 1 << 20, MaxInflated: 1 << 20})
			if err != nil || got.Digest != d.Digest || got.Encoded != d.Size {
				t.Fatalf("native identity: %+v %v", got, err)
			}
			if !got.Opaque && got.DiffID != Sum256(plain) {
				t.Fatal("DiffID differs")
			}
			bad := append([]byte(nil), tc.data...)
			bad[len(bad)/2] ^= 1
			if _, err := VerifyLayer(bytes.NewReader(bad), d, Sum256(plain), Limits{}); err == nil {
				t.Fatal("corrupt layer accepted")
			}
			if !got.Opaque {
				if _, err := VerifyLayer(bytes.NewReader(tc.data), d, Sum256([]byte("wrong")), Limits{}); !errors.Is(err, ErrDigest) {
					t.Fatalf("wrong DiffID: %v", err)
				}
				if _, err := VerifyLayer(bytes.NewReader(tc.data), d, Sum256(plain), Limits{MaxInflated: 128}); err == nil {
					t.Fatal("inflation bound ignored")
				}
			}
		})
	}
	// A digest of the bounded prefix must never masquerade as the full DiffID.
	node := &Node{Kind: KindLayerGz, Data: gz.Bytes(), DiffID: Sum256(plain[:129])}
	if VerifyDiffID(node, Limits{MaxInflated: 128}) == "ok" {
		t.Fatal("truncated prefix accepted as DiffID")
	}
}
