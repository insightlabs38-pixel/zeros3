// Package parity is an isolated single-loss repair experiment for exact chunks.
package parity

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io/fs"
)

const (
	magic       = "ZS3PAR1\n"
	memberBytes = 4 + sha256.Size
	maxMembers  = 32
	maxChunk    = 16 << 20
)

type ID = [32]byte

var (
	ErrHealthy  = errors.New("parity group requires no repair")
	ErrMultiple = errors.New("parity group contains more than one unavailable chunk")
	ErrCorrupt  = errors.New("invalid parity data")
)

// Encode records each member's digest and length and one XOR recovery shard.
func Encode(chunks [][]byte) ([]byte, error) {
	if len(chunks) < 2 || len(chunks) > maxMembers {
		return nil, ErrCorrupt
	}
	size := 0
	seen := make(map[ID]bool, len(chunks))
	out := binary.LittleEndian.AppendUint16([]byte(magic), uint16(len(chunks)))
	for _, chunk := range chunks {
		sum := sha256.Sum256(chunk)
		if len(chunk) == 0 || len(chunk) > maxChunk || seen[sum] {
			return nil, ErrCorrupt
		}
		size = max(size, len(chunk))
		seen[sum] = true
		out = binary.LittleEndian.AppendUint32(out, uint32(len(chunk)))
		out = append(out, sum[:]...)
	}
	start := len(out)
	out = append(out, make([]byte, size)...)
	for _, chunk := range chunks {
		parity := out[start : start+len(chunk)]
		subtle.XORBytes(parity, parity, chunk)
	}
	sum := sha256.Sum256(out)
	return append(out, sum[:]...), nil
}

// Repair returns one independently verified missing or corrupt chunk.
// Other I/O failures leave the group unchanged for a later attempt.
func Repair(wire []byte, get func(ID) ([]byte, error)) (ID, []byte, error) {
	var zero ID
	const header = len(magic) + 2
	if len(wire) < header+2*memberBytes+sha256.Size || string(wire[:len(magic)]) != magic {
		return zero, nil, ErrCorrupt
	}
	n := int(binary.LittleEndian.Uint16(wire[len(magic):header]))
	metaEnd := header + n*memberBytes
	if n < 2 || n > maxMembers || metaEnd > len(wire)-sha256.Size {
		return zero, nil, ErrCorrupt
	}
	end := len(wire) - sha256.Size
	payload := wire[metaEnd:end]
	if len(payload) == 0 || len(payload) > maxChunk {
		return zero, nil, ErrCorrupt
	}
	sum := sha256.Sum256(wire[:end])
	if !bytes.Equal(sum[:], wire[end:]) {
		return zero, nil, ErrCorrupt
	}
	seen := make(map[ID]bool, n)
	largest, missingSize := 0, 0
	var missing ID
	decoded := bytes.Clone(payload)
	for i := range n {
		row := wire[header+i*memberBytes:][:memberBytes]
		size := int(binary.LittleEndian.Uint32(row))
		id := ID(row[4:])
		if size == 0 || size > len(decoded) || seen[id] {
			return zero, nil, ErrCorrupt
		}
		largest = max(largest, size)
		seen[id] = true
		data, err := get(id)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return zero, nil, err
		}
		if err != nil || len(data) != size || sha256.Sum256(data) != id {
			if missingSize != 0 {
				return zero, nil, ErrMultiple
			}
			missing, missingSize = id, size
			continue
		}
		subtle.XORBytes(decoded[:size], decoded[:size], data)
	}
	if largest != len(payload) {
		return zero, nil, ErrCorrupt
	}
	if missingSize == 0 {
		return zero, nil, ErrHealthy
	}
	if sha256.Sum256(decoded[:missingSize]) != missing {
		return zero, nil, ErrCorrupt
	}
	return missing, decoded[:missingSize], nil
}
