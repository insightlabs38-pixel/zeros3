package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
)

// The prototype shares transaction payloads and the ack-after-Sync edge
// across all candidates. No prototype files are opened by Store.
const (
	fbj2Begin     = 64
	fbj2Footer    = 64
	fbj2Slot      = 512
	fbj2MaxGroup  = 32 << 20
	fbj2MaxFrames = 4096
)

type fbj2Cursor struct {
	end, count, sequence uint64
	digest               [32]byte
}

type fbj2Proto struct {
	file          *os.File
	mode          byte
	pos           fbj2Cursor
	compactCRC    uint32
	writes, syncs int
}

func fbj2Create(path string, mode byte) (*fbj2Proto, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	p := &fbj2Proto{file: f, mode: mode}
	if mode != 'B' {
		p.pos.end = v2HeaderSize
		if err := f.Truncate(v2HeaderSize); err != nil {
			return nil, errors.Join(err, f.Close())
		}
	}
	if mode == 'C' {
		if _, err := f.WriteAt(fbj2Witness(p.pos), 0); err != nil {
			return nil, errors.Join(err, f.Close())
		}
	}
	if err := f.Sync(); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return p, nil
}

func fbj2Witness(pos fbj2Cursor) []byte {
	b := make([]byte, fbj2Slot)
	copy(b, "J2HW")
	binary.BigEndian.PutUint64(b[8:], pos.end)
	binary.BigEndian.PutUint64(b[16:], pos.count)
	binary.BigEndian.PutUint64(b[24:], pos.sequence)
	copy(b[32:], pos.digest[:])
	binary.BigEndian.PutUint32(b[fbj2Slot-4:], crc32.Checksum(b[:fbj2Slot-4], castagnoliTable))
	return b
}

func fbj2ReadWitness(image []byte, slot int) (fbj2Cursor, bool) {
	off := slot * v2SlotStride
	if len(image)-off < fbj2Slot {
		return fbj2Cursor{}, false
	}
	b := image[off : off+fbj2Slot]
	if string(b[:4]) != "J2HW" ||
		binary.BigEndian.Uint32(b[fbj2Slot-4:]) != crc32.Checksum(b[:fbj2Slot-4], castagnoliTable) {
		return fbj2Cursor{}, false
	}
	pos := fbj2Cursor{
		end: binary.BigEndian.Uint64(b[8:]), count: binary.BigEndian.Uint64(b[16:]),
		sequence: binary.BigEndian.Uint64(b[24:]),
	}
	copy(pos.digest[:], b[32:])
	return pos, pos.count%2 == uint64(slot) && pos.end >= v2HeaderSize
}

func fbj2FramedSize(payloads [][]byte) int {
	total := fbj2Begin + fbj2Footer
	if len(payloads) == 0 || len(payloads) > fbj2MaxFrames {
		return 0
	}
	for _, payload := range payloads {
		if len(payload) > maxJournalPayload || total > fbj2MaxGroup-len(payload)-journalHeaderSize-4 {
			return 0
		}
		total += len(payload) + journalHeaderSize + 4
	}
	return total
}

func fbj2Build(pos fbj2Cursor, payloads [][]byte, total int) ([]byte, fbj2Cursor) {
	b := make([]byte, fbj2Begin, total)
	copy(b, "J2BG")
	binary.BigEndian.PutUint32(b[4:], uint32(total))
	binary.BigEndian.PutUint64(b[8:], pos.count+1)
	binary.BigEndian.PutUint64(b[16:], pos.sequence+1)
	binary.BigEndian.PutUint32(b[24:], uint32(len(payloads)))
	copy(b[28:60], pos.digest[:])
	binary.BigEndian.PutUint32(b[60:], crc32.Checksum(b[:60], castagnoliTable))
	for i, payload := range payloads {
		b = fbj2AppendFrame(b, pos.sequence+uint64(i)+1, payload)
	}
	var footer [fbj2Footer]byte
	copy(footer[:], "J2FT")
	binary.BigEndian.PutUint32(footer[4:], uint32(len(payloads)))
	binary.BigEndian.PutUint64(footer[8:], pos.end)
	binary.BigEndian.PutUint64(footer[16:], pos.count+1)
	binary.BigEndian.PutUint64(footer[24:], pos.sequence+uint64(len(payloads)))
	b = append(b, footer[:32]...)
	digest := sha256.Sum256(b)
	b = append(b, digest[:]...)
	return b, fbj2Cursor{end: pos.end + uint64(len(b)), count: pos.count + 1,
		sequence: pos.sequence + uint64(len(payloads)), digest: digest}
}

func fbj2AppendFrame(dst []byte, sequence uint64, payload []byte) []byte {
	start := len(dst)
	dst = append(dst, journalMagic...)
	dst = binary.BigEndian.AppendUint16(dst, journalFrameVersion)
	dst = append(dst, recordTypePutObjectRoot, 0)
	dst = binary.BigEndian.AppendUint64(dst, sequence)
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(payload)))
	dst = append(dst, payload...)
	return binary.BigEndian.AppendUint32(dst, crc32.Checksum(dst[start:], castagnoliTable))
}

// A group bounds the entire frame region. Validating the slice avoids a
// buffered reader allocation for every small transaction during recovery.
func fbj2FramesValid(body []byte, first, count uint64) bool {
	for i := range count {
		if len(body) < journalHeaderSize+4 {
			return false
		}
		n := uint64(binary.BigEndian.Uint32(body[16:20]))
		end := n + journalHeaderSize + 4
		if n > maxJournalPayload || end > uint64(len(body)) || string(body[:4]) != journalMagic ||
			binary.BigEndian.Uint16(body[4:6]) != journalFrameVersion || !journalKnownType(body[6]) ||
			binary.BigEndian.Uint64(body[8:16]) != first+i ||
			binary.BigEndian.Uint32(body[end-4:end]) != crc32.Checksum(body[:end-4], castagnoliTable) {
			return false
		}
		body = body[end:]
	}
	return len(body) == 0
}

func (p *fbj2Proto) append(payloads [][]byte) error {
	total := fbj2FramedSize(payloads)
	if total == 0 {
		return errors.New("invalid prototype group size")
	}
	var body []byte
	var next fbj2Cursor
	var compactCRC uint32
	if p.mode == 'A' {
		// The compact marker uses the old Design B record serializer,
		// without its post-flush sidecar or authoritative seal.
		old := v2Journal{end: int64(p.pos.end), seq: p.pos.sequence, group: p.pos.count, chain: p.compactCRC}
		body = old.groupBytes(payloads)
		compactCRC = crc32.Update(p.compactCRC, castagnoliTable, body[:len(body)-journalHeaderSize-v2CommitLen-4])
		next = fbj2Cursor{end: p.pos.end + uint64(len(body)), count: p.pos.count + 1,
			sequence: p.pos.sequence + uint64(len(payloads)) + 1}
	} else {
		body, next = fbj2Build(p.pos, payloads, total)
	}
	if _, err := p.file.WriteAt(body, int64(p.pos.end)); err != nil {
		return err
	}
	p.writes++
	if p.mode == 'C' {
		if _, err := p.file.WriteAt(fbj2Witness(next), int64(next.count%2)*v2SlotStride); err != nil {
			return err
		}
		p.writes++
	}
	if err := p.file.Sync(); err != nil {
		return err
	}
	p.syncs++
	p.pos, p.compactCRC = next, compactCRC
	return nil
}

func fbj2Decode(image []byte, pos fbj2Cursor) (fbj2Cursor, bool) {
	if pos.end > uint64(len(image)) || uint64(len(image))-pos.end < fbj2Begin {
		return fbj2Cursor{}, false
	}
	h := image[pos.end : pos.end+fbj2Begin]
	total, records := uint64(binary.BigEndian.Uint32(h[4:])), uint64(binary.BigEndian.Uint32(h[24:]))
	if string(h[:4]) != "J2BG" || total < fbj2Begin+fbj2Footer || total > fbj2MaxGroup ||
		total > uint64(len(image))-pos.end || records == 0 || records > fbj2MaxFrames ||
		binary.BigEndian.Uint64(h[8:]) != pos.count+1 ||
		binary.BigEndian.Uint64(h[16:]) != pos.sequence+1 || !bytes.Equal(h[28:60], pos.digest[:]) ||
		binary.BigEndian.Uint32(h[60:]) != crc32.Checksum(h[:60], castagnoliTable) {
		return fbj2Cursor{}, false
	}
	end := pos.end + total
	f := image[end-fbj2Footer : end]
	digest := sha256.Sum256(image[pos.end : end-32])
	if string(f[:4]) != "J2FT" || binary.BigEndian.Uint32(f[4:]) != uint32(records) ||
		binary.BigEndian.Uint64(f[8:]) != pos.end || binary.BigEndian.Uint64(f[16:]) != pos.count+1 ||
		binary.BigEndian.Uint64(f[24:]) != pos.sequence+records || !bytes.Equal(f[32:], digest[:]) {
		return fbj2Cursor{}, false
	}
	if !fbj2FramesValid(image[pos.end+fbj2Begin:end-fbj2Footer], pos.sequence+1, records) {
		return fbj2Cursor{}, false
	}
	return fbj2Cursor{end: end, count: pos.count + 1, sequence: pos.sequence + records, digest: digest}, true
}

func fbj2LaterGroup(image []byte, pos fbj2Cursor) bool {
	for from := int(pos.end) + 1; from <= len(image)-fbj2Begin; {
		rel := bytes.Index(image[from:], []byte("J2BG"))
		if rel < 0 || from+rel > len(image)-fbj2Begin {
			return false
		}
		off := from + rel
		h := image[off : off+fbj2Begin]
		group := binary.BigEndian.Uint64(h[8:])
		first := binary.BigEndian.Uint64(h[16:])
		if group > pos.count+1 && first > pos.sequence {
			prior := fbj2Cursor{end: uint64(off), count: group - 1, sequence: first - 1}
			copy(prior.digest[:], h[28:60])
			if _, ok := fbj2Decode(image, prior); ok {
				return true
			}
		}
		from = off + 1
	}
	return false
}

func fbj2Recover(mode byte, image []byte) (fbj2Cursor, string) {
	if mode == 'A' {
		groups, end := v2Scan(image)
		pos := fbj2Cursor{end: uint64(end), count: uint64(len(groups))}
		if v2LaterCommit(image, end, pos.count+1) {
			return pos, "refuse-later"
		}
		if end == int64(len(image)) {
			return pos, "clean"
		}
		return pos, "recover"
	}
	var pos fbj2Cursor
	var witnesses [2]fbj2Cursor
	var marked [2]bool
	var witnessed [2]bool
	if mode == 'C' {
		if len(image) < v2HeaderSize {
			return pos, "refuse-header"
		}
		pos.end = v2HeaderSize
		for i := range witnesses {
			witnesses[i], marked[i] = fbj2ReadWitness(image, i)
			witnessed[i] = marked[i] && witnesses[i] == pos
		}
		if !marked[0] && !marked[1] {
			return pos, "refuse-header"
		}
	}
	for {
		next, ok := fbj2Decode(image, pos)
		if !ok {
			break
		}
		pos = next
		for i := range witnesses {
			witnessed[i] = witnessed[i] || marked[i] && witnesses[i] == pos
		}
	}
	for i := range witnesses {
		if marked[i] && !witnessed[i] {
			return pos, "refuse-witness"
		}
	}
	if fbj2LaterGroup(image, pos) {
		return pos, "refuse-later"
	}
	if pos.end == uint64(len(image)) {
		return pos, "clean"
	}
	if mode == 'C' && !(marked[0] && marked[1]) {
		return pos, "refuse-degraded"
	}
	return pos, "recover"
}
