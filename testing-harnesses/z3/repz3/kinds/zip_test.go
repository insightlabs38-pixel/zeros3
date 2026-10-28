package kinds

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"testing"
	"zeros3-z3/torchz3"
)

const (
	zipFramesigLocal   = 0x04034b50
	zipFramesigCentral = 0x02014b50
	zipFramesigEOCD    = 0x06054b50
	zipFramesigEOCD64  = 0x06064b50
	zipFramesigLoc64   = 0x07064b50
	zipFramesigDesc    = 0x08074b50
)

type zipFrameMember struct {
	name string
	data []byte
}

func zipFrameLE(b []byte, v uint64, n int) []byte {
	for i := 0; i < n; i++ {
		b = append(b, byte(v>>(8*i)))
	}
	return b
}

// Fixture framing is mechanically reused from the strict ZIP64 parser tests: stored members, flags 0x0808, a local extra
// field that pads the data to a 64-byte boundary, a data descriptor with signature and 4-byte
// sizes (8-byte when zip64Desc), a central directory, a ZIP64 end record and locator, an EOCD.
func buildZIPFrame(ms []zipFrameMember, zip64Desc bool) []byte {
	var out, cd []byte
	for _, m := range ms {
		off := len(out)
		hl := 30 + len(m.name) + 4
		pad := (64 - (off+hl)%64) % 64
		xl := 4 + pad
		crc := crc32.ChecksumIEEE(m.data)
		out = zipFrameLE(out, zipFramesigLocal, 4)
		out = zipFrameLE(out, 0, 2)      // version
		out = zipFrameLE(out, 0x0808, 2) // flags
		out = zipFrameLE(out, 0, 2)      // method
		out = zipFrameLE(out, 0, 4)      // time
		out = zipFrameLE(out, 0, 4)      // crc (in descriptor)
		out = zipFrameLE(out, 0, 8)      // sizes
		out = zipFrameLE(out, uint64(len(m.name)), 2)
		out = zipFrameLE(out, uint64(xl), 2)
		out = append(out, m.name...)
		out = append(out, 'F', 'B')
		out = zipFrameLE(out, uint64(pad), 2)
		out = append(out, bytes.Repeat([]byte{'Z'}, pad)...)
		out = append(out, m.data...)
		out = zipFrameLE(out, zipFramesigDesc, 4)
		out = zipFrameLE(out, uint64(crc), 4)
		w := 4
		if zip64Desc {
			w = 8
		}
		out = zipFrameLE(out, uint64(len(m.data)), w)
		out = zipFrameLE(out, uint64(len(m.data)), w)
		cd = zipFrameLE(cd, zipFramesigCentral, 4)
		cd = zipFrameLE(cd, 0, 2)
		cd = zipFrameLE(cd, 0, 2)
		cd = zipFrameLE(cd, 0x0808, 2)
		cd = zipFrameLE(cd, 0, 2)
		cd = zipFrameLE(cd, 0, 4)
		cd = zipFrameLE(cd, uint64(crc), 4)
		if zip64Desc {
			cd = zipFrameLE(cd, 0xffffffff, 4)
			cd = zipFrameLE(cd, 0xffffffff, 4)
		} else {
			cd = zipFrameLE(cd, uint64(len(m.data)), 4)
			cd = zipFrameLE(cd, uint64(len(m.data)), 4)
		}
		cd = zipFrameLE(cd, uint64(len(m.name)), 2)
		xlen := 0
		if zip64Desc {
			xlen = 4 + 16
		}
		cd = zipFrameLE(cd, uint64(xlen), 2)
		cd = zipFrameLE(cd, 0, 2)
		cd = zipFrameLE(cd, 0, 2)
		cd = zipFrameLE(cd, 0, 2)
		cd = zipFrameLE(cd, 0, 4)
		cd = zipFrameLE(cd, uint64(off), 4)
		cd = append(cd, m.name...)
		if zip64Desc {
			cd = zipFrameLE(cd, 1, 2)
			cd = zipFrameLE(cd, 16, 2)
			cd = zipFrameLE(cd, uint64(len(m.data)), 8)
			cd = zipFrameLE(cd, uint64(len(m.data)), 8)
		}
	}
	cdOff := len(out)
	out = append(out, cd...)
	z64 := len(out)
	out = zipFrameLE(out, zipFramesigEOCD64, 4)
	out = zipFrameLE(out, 44, 8)
	out = zipFrameLE(out, 0x2d, 2)
	out = zipFrameLE(out, 0x2d, 2)
	out = zipFrameLE(out, 0, 4)
	out = zipFrameLE(out, 0, 4)
	out = zipFrameLE(out, uint64(len(ms)), 8)
	out = zipFrameLE(out, uint64(len(ms)), 8)
	out = zipFrameLE(out, uint64(len(cd)), 8)
	out = zipFrameLE(out, uint64(cdOff), 8)
	out = zipFrameLE(out, zipFramesigLoc64, 4)
	out = zipFrameLE(out, 0, 4)
	out = zipFrameLE(out, uint64(z64), 8)
	out = zipFrameLE(out, 1, 4)
	out = zipFrameLE(out, zipFramesigEOCD, 4)
	out = zipFrameLE(out, 0, 4)
	out = zipFrameLE(out, uint64(len(ms)), 2)
	out = zipFrameLE(out, uint64(len(ms)), 2)
	out = zipFrameLE(out, uint64(len(cd)), 4)
	out = zipFrameLE(out, uint64(cdOff), 4)
	out = zipFrameLE(out, 0, 2)
	return out
}

func makeZIPFrame(t *testing.T, names []string, files map[string][]byte, raw bool) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, name := range names {
		method := uint16(zip.Store)
		if name == "asset.txt" {
			method = zip.Deflate
		}
		h := &zip.FileHeader{Name: name, Method: method}
		var member io.Writer
		var err error
		if raw {
			h.Method = zip.Store
			h.CRC32 = crc32.ChecksumIEEE(files[name])
			h.CompressedSize64 = uint64(len(files[name]))
			h.UncompressedSize64 = uint64(len(files[name]))
			member, err = w.CreateRaw(h)
		} else {
			member, err = w.CreateHeader(h)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err = member.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func TestZIPExistingPrimitives(t *testing.T) {
	db := SQLiteChain(32).Stages[0].Data
	files := map[string][]byte{"app.db": db, "asset.txt": bytes.Repeat([]byte("compressed bytes remain opaque\n"), 1000)}
	normal := makeZIPFrame(t, []string{"app.db", "asset.txt"}, files, false)
	reversed := makeZIPFrame(t, []string{"asset.txt", "app.db"}, files, false)
	raw := makeZIPFrame(t, []string{"app.db", "asset.txt"}, files, true)
	end := bytes.LastIndex(normal, []byte{'P', 'K', 5, 6})
	cd := int(binary.LittleEndian.Uint32(normal[end+16:]))
	signing := append(bytes.Repeat([]byte{0x9a}, 64), []byte("APK Sig Block 42")...)
	signed := append(append(append([]byte(nil), normal[:cd]...), signing...), normal[cd:]...)
	binary.LittleEndian.PutUint32(signed[end+len(signing)+16:], uint32(cd+len(signing)))
	fixtures := map[string][]byte{"stored and DEFLATE descriptors": normal, "member reorder": reversed, "no descriptor": raw, "opaque signing material": signed, "ZIP64 descriptor32": buildZIPFrame([]zipFrameMember{{"app.db", db}, {"asset.txt", files["asset.txt"]}}, false), "ZIP64 descriptor64": buildZIPFrame([]zipFrameMember{{"app.db", db}, {"asset.txt", files["asset.txt"]}}, true)}
	for name, data := range fixtures {
		t.Run(name, func(t *testing.T) {
			bundle, st, err := EncodeZIPAt(bytes.NewReader(data), int64(len(data)), func(b []byte) (*Bundle, error) {
				out, _, err := EncodeSQLite(bytes.NewReader(b), int64(len(b)))
				return out, err
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := Prove(nil, bundle, data); err != nil {
				t.Fatal(err)
			}
			if st.Framing+st.Payload != int64(len(data)) {
				t.Fatalf("uncharged bytes: %+v", st)
			}
			zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range zr.File {
				r, err := f.Open()
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(r)
				r.Close()
				if err != nil || !bytes.Equal(got, files[f.Name]) {
					t.Fatalf("oracle payload %s: %v", f.Name, err)
				}
			}
			if st.Derived == 0 {
				t.Fatal("stored SQLite opportunity not represented")
			}
			t.Logf("SYNTHETIC_FIXTURE members=%d stored=%d deflated=%d derived=%d framing=%d payload=%d", st.Members, st.Stored, st.Deflated, st.Derived, st.Framing, st.Payload)
		})
	}
	if _, err := torchz3.Parse(bytes.NewReader(normal), int64(len(normal)), torchz3.Limits{}); !errors.Is(err, torchz3.ErrUnsupported) {
		t.Fatal("legacy torch parser default was broadened")
	}
}
