// Package namespace provides an isolated, immutable, disk-backed namespace experiment.
package namespace

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

const (
	magic         = "ZS3NSL01"
	headerBytes   = len(magic) + 8 + sha256.Size
	recordsPerRun = 256
	maxKey        = 2048
	maxBlockBytes = recordsPerRun * (maxKey + sha256.Size + 14)
	maxIndexBytes = 64 << 20
)

type Row struct {
	Key    string
	Digest [sha256.Size]byte
	Size   uint64
}

type block struct {
	Key    string
	Offset int64
	Size   int
	Digest [sha256.Size]byte
}

type Table struct {
	src    io.ReaderAt
	blocks []block
}

// Build writes ordered, unique rows. basis binds the derived file to an independently verified fixture.
func Build(dst io.WriteSeeker, basis [32]byte, next func() (Row, error)) error {
	header := make([]byte, headerBytes)
	copy(header, magic)
	copy(header[len(magic)+8:], basis[:])
	if err := write(dst, header); err != nil {
		return err
	}
	var catalog []byte
	var blockRows []byte
	var first, last string
	offset := int64(headerBytes)
	lines := 0
	flush := func() error {
		if lines == 0 {
			return nil
		}
		sum := sha256.Sum256(blockRows)
		catalog = binary.AppendUvarint(catalog, uint64(len(first)))
		catalog = append(catalog, first...)
		catalog = binary.AppendUvarint(catalog, uint64(offset))
		catalog = binary.AppendUvarint(catalog, uint64(len(blockRows)))
		catalog = append(catalog, sum[:]...)
		if len(catalog) > maxIndexBytes {
			return errors.New("table index exceeds limit")
		}
		if err := write(dst, blockRows); err != nil {
			return err
		}
		offset += int64(len(blockRows))
		lines = 0
		blockRows = blockRows[:0]
		return nil
	}
	for {
		row, err := next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if row.Key == "" || row.Key <= last || len(row.Key) > maxKey {
			return errors.New("namespace rows must have nonempty, bounded, unique increasing keys")
		}
		prefix := 0
		if lines == 0 {
			first = row.Key
		} else {
			for prefix < len(row.Key) && prefix < len(last) && row.Key[prefix] == last[prefix] {
				prefix++
			}
		}
		suffix := row.Key[prefix:]
		blockRows = binary.LittleEndian.AppendUint16(blockRows, uint16(prefix))
		blockRows = binary.LittleEndian.AppendUint16(blockRows, uint16(len(suffix)))
		blockRows = append(blockRows, suffix...)
		blockRows = append(blockRows, row.Digest[:]...)
		blockRows = binary.AppendUvarint(blockRows, row.Size)
		last = row.Key
		lines++
		if lines == recordsPerRun {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	binary.LittleEndian.PutUint64(header[len(magic):], uint64(offset))
	check := sha256.New()
	check.Write(header)
	check.Write(catalog)
	if err := write(dst, catalog); err != nil {
		return err
	}
	if err := write(dst, check.Sum(nil)); err != nil {
		return err
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return write(dst, header)
}

func write(dst io.Writer, data []byte) error {
	n, err := dst.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

func Open(src io.ReaderAt, size int64, basis [32]byte) (*Table, error) {
	var header [headerBytes]byte
	if size < int64(headerBytes+sha256.Size) {
		return nil, errors.New("short table")
	}
	if _, err := src.ReadAt(header[:], 0); err != nil || string(header[:len(magic)]) != magic ||
		!bytes.Equal(header[len(magic)+8:], basis[:]) {
		return nil, errors.New("table identity mismatch")
	}
	dir := int64(binary.LittleEndian.Uint64(header[len(magic):]))
	if dir < int64(headerBytes) || dir > size-sha256.Size || size-dir-sha256.Size > maxIndexBytes {
		return nil, errors.New("invalid index extent")
	}
	b := make([]byte, size-dir)
	if _, err := src.ReadAt(b, dir); err != nil {
		return nil, err
	}
	payload, want := b[:len(b)-sha256.Size], b[len(b)-sha256.Size:]
	check := sha256.New()
	check.Write(header[:])
	check.Write(payload)
	if !bytes.Equal(check.Sum(nil), want) {
		return nil, errors.New("table index checksum mismatch")
	}
	t := &Table{src: src}
	expected := int64(headerBytes)
	prev := ""
	input := bytes.NewReader(payload)
	for input.Len() != 0 {
		n, err := binary.ReadUvarint(input)
		if err != nil || n == 0 || n > maxKey || n > uint64(input.Len()) {
			return nil, errors.New("invalid index key")
		}
		key := make([]byte, int(n))
		if _, err := io.ReadFull(input, key); err != nil {
			return nil, err
		}
		off, err := binary.ReadUvarint(input)
		if err != nil || off != uint64(expected) {
			return nil, errors.New("invalid index offset")
		}
		length, err := binary.ReadUvarint(input)
		if err != nil || length == 0 || length > maxBlockBytes || length > uint64(dir-expected) || string(key) <= prev {
			return nil, errors.New("invalid index geometry")
		}
		entry := block{Key: string(key), Offset: expected, Size: int(length)}
		if _, err := io.ReadFull(input, entry.Digest[:]); err != nil {
			return nil, err
		}
		t.blocks = append(t.blocks, entry)
		expected += int64(length)
		prev = entry.Key
	}
	if expected != dir {
		return nil, errors.New("index does not cover data section")
	}
	return t, nil
}

func (t *Table) read(b block, visit func([]byte, [32]byte, uint64) bool) error {
	raw := make([]byte, b.Size)
	if _, err := t.src.ReadAt(raw, b.Offset); err != nil {
		return err
	}
	if sha256.Sum256(raw) != b.Digest {
		return errors.New("namespace block checksum mismatch")
	}
	key := make([]byte, 0, 128)
	for len(raw) != 0 {
		if len(raw) < 4+sha256.Size+1 {
			return errors.New("short namespace row")
		}
		prefix, suffix := int(binary.LittleEndian.Uint16(raw)), int(binary.LittleEndian.Uint16(raw[2:]))
		raw = raw[4:]
		if prefix > len(key) || suffix == 0 || suffix > len(raw)-sha256.Size-1 || prefix+suffix > maxKey {
			return errors.New("invalid namespace key prefix")
		}
		key = append(key[:prefix], raw[:suffix]...)
		raw = raw[suffix:]
		var digest [32]byte
		copy(digest[:], raw)
		size, n := binary.Uvarint(raw[sha256.Size:])
		if n <= 0 {
			return errors.New("invalid namespace row size")
		}
		raw = raw[sha256.Size+n:]
		if !visit(key, digest, size) {
			return nil
		}
	}
	return nil
}

func (t *Table) Lookup(key string) (row Row, found bool, err error) {
	i, equal := slices.BinarySearchFunc(t.blocks, key, func(e block, k string) int {
		return strings.Compare(e.Key, k)
	})
	if !equal {
		i--
	}
	if i < 0 {
		return
	}
	err = t.read(t.blocks[i], func(candidate []byte, digest [32]byte, size uint64) bool {
		order := bytes.Compare(candidate, []byte(key))
		found = order == 0
		if found {
			row = Row{Key: key, Digest: digest, Size: size}
		}
		return order < 0
	})
	return
}

func (t *Table) Scan(visit func(Row) bool) error {
	for _, b := range t.blocks {
		more := true
		err := t.read(b, func(key []byte, digest [32]byte, size uint64) bool {
			more = visit(Row{Key: string(key), Digest: digest, Size: size})
			return more
		})
		if err != nil {
			return fmt.Errorf("%q: %w", b.Key, err)
		}
		if !more {
			break
		}
	}
	return nil
}
