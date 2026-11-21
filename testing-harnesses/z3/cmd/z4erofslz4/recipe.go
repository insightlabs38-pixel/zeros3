package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"zeros3-z3/lz4z3"
)

const format = "Z3ER1"

type receipt struct {
	SourceSHA256 string `json:"source_sha256"`
	ImageSHA256  string `json:"image_sha256"`
	Clusters     int    `json:"recipe_clusters"`
	LiteralBytes int64  `json:"literal_bytes"`
}

func digestFile(f *os.File) ([32]byte, int64, error) {
	var stamp [32]byte
	st, err := f.Stat()
	if err != nil {
		return stamp, 0, err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.NewSectionReader(f, 0, st.Size()))
	if err != nil || n != st.Size() {
		return stamp, 0, errors.New("incomplete input file")
	}
	h.Sum(stamp[:0])
	return stamp, n, nil
}

func makeRecipe(image, source *os.File, spans []extent, dst io.Writer) (receipt, error) {
	var result receipt
	trace := bufio.NewWriterSize(dst, 128<<10)
	sourceSHA, sourceSize, err := digestFile(source)
	if err != nil {
		return result, err
	}
	imageSHA, imageSize, err := digestFile(image)
	if err != nil {
		return result, err
	}
	result.SourceSHA256, result.ImageSHA256 = fmt.Sprintf("%x", sourceSHA), fmt.Sprintf("%x", imageSHA)
	header := binary.LittleEndian.AppendUint64([]byte(format), uint64(imageSize))
	header = append(header, sourceSHA[:]...)
	header = append(header, imageSHA[:]...)
	if _, err := trace.Write(header); err != nil {
		return result, err
	}
	var pos int64
	literal := func(end int64) error {
		n := end - pos
		if n == 0 {
			return nil
		}
		h := binary.AppendUvarint([]byte{0}, uint64(n))
		if _, err := trace.Write(h); err != nil {
			return err
		}
		got, err := io.Copy(trace, io.NewSectionReader(image, pos, n))
		if err != nil || got != n {
			return errors.New("incomplete literal frame")
		}
		pos = end
		result.LiteralBytes += n
		return nil
	}
	slices.SortFunc(spans, func(a, b extent) int {
		if a.Physical < b.Physical {
			return -1
		}
		if a.Physical > b.Physical {
			return 1
		}
		return 0
	})
	limits := lz4z3.Limits{}
	classifier := lz4z3.Analyzer{GiveUp: 24}
	var block [4096]byte
	for _, x := range spans {
		if x.Physical < pos || x.Physical > imageSize-int64(x.Stored) || x.Logical > sourceSize-int64(x.Plain) {
			continue
		}
		plain := make([]byte, x.Plain)
		if _, err := image.ReadAt(block[:], x.Physical); err != nil {
			return result, err
		}
		if _, err := source.ReadAt(plain, x.Logical); err != nil {
			return result, err
		}
		compressed := bytes.TrimLeft(block[:], "\x00")
		decoded, seqs, err := lz4z3.Decode(compressed, x.Plain, limits)
		if err != nil || !bytes.Equal(decoded, plain) {
			continue
		}
		recipe := classifier.Analyze(plain, seqs, true)
		output, err := recipe.Replay(nil, plain, limits)
		if err != nil || !bytes.Equal(output, compressed) {
			return result, fmt.Errorf("cluster %d differs during recipe construction: %v", x.Physical, err)
		}
		encoded := recipe.Marshal()
		if err := literal(x.Physical); err != nil {
			return result, err
		}
		h := binary.AppendUvarint([]byte{1}, uint64(x.Logical))
		h = binary.AppendUvarint(h, uint64(x.Plain))
		h = binary.AppendUvarint(h, uint64(len(block)-len(compressed)))
		h = binary.AppendUvarint(h, uint64(len(encoded)))
		if _, err := trace.Write(h); err != nil {
			return result, err
		}
		if _, err := trace.Write(encoded); err != nil {
			return result, err
		}
		pos += int64(len(block))
		result.Clusters++
	}
	if err := literal(imageSize); err != nil {
		return result, err
	}
	return result, trace.Flush()
}

func replayRecipe(recipe io.Reader, source *os.File, dst io.Writer) error {
	input := bufio.NewReaderSize(recipe, 128<<10)
	var header [len(format) + 8 + 2*sha256.Size]byte
	if _, err := io.ReadFull(input, header[:]); err != nil || string(header[:len(format)]) != format {
		return errors.New("invalid EROFS recipe header")
	}
	size := int64(binary.LittleEndian.Uint64(header[len(format):]))
	if size < 0 || size > 1<<40 {
		return errors.New("invalid EROFS recipe size")
	}
	digest, sourceSize, err := digestFile(source)
	if err != nil || !bytes.Equal(digest[:], header[len(format)+8:len(format)+8+sha256.Size]) {
		return errors.New("wrong EROFS recipe basis")
	}
	h := sha256.New()
	output := io.MultiWriter(dst, h)
	lim := lz4z3.Limits{}
	var plain []byte
	var stored [4096]byte
	for size != 0 {
		tag, err := input.ReadByte()
		if err != nil {
			return err
		}
		switch tag {
		case 0:
			n, err := binary.ReadUvarint(input)
			if err != nil || n == 0 || n > uint64(size) {
				return errors.New("invalid EROFS literal frame")
			}
			if _, err := io.CopyN(output, input, int64(n)); err != nil {
				return err
			}
			size -= int64(n)
		case 1:
			var v [4]uint64
			for i := range v {
				v[i], err = binary.ReadUvarint(input)
				if err != nil {
					return err
				}
			}
			offset, n, padding, recipeBytes := v[0], v[1], v[2], v[3]
			if size < int64(len(stored)) || n == 0 || n > 1<<20 || padding >= uint64(len(stored)) ||
				recipeBytes > 1<<20 || offset > uint64(sourceSize) || n > uint64(sourceSize)-offset {
				return errors.New("invalid compressed EROFS frame")
			}
			encoded := make([]byte, int(recipeBytes))
			if _, err := io.ReadFull(input, encoded); err != nil {
				return err
			}
			decoded, used, err := lz4z3.Unmarshal(encoded, lim)
			if err != nil || used != len(encoded) {
				return errors.New("malformed EROFS LZ4 recipe")
			}
			plain = slices.Grow(plain[:0], int(n))[:n]
			if _, err := source.ReadAt(plain, int64(offset)); err != nil {
				return err
			}
			compressed, err := decoded.Replay(nil, plain, lim)
			if err != nil || len(compressed) != len(stored)-int(padding) {
				return errors.New("replayed EROFS cluster size mismatch")
			}
			clear(stored[:padding])
			copy(stored[padding:], compressed)
			if _, err := output.Write(stored[:]); err != nil {
				return err
			}
			size -= int64(len(stored))
		default:
			return errors.New("unknown EROFS recipe frame")
		}
	}
	if _, err := input.ReadByte(); err != io.EOF || !bytes.Equal(h.Sum(nil), header[len(format)+8+sha256.Size:]) {
		return errors.New("EROFS recipe digest or end marker mismatch")
	}
	return nil
}
