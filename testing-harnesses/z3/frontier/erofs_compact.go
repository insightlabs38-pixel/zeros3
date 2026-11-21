package frontier

import (
	"encoding/binary"
	"fmt"
	"io"
)

const erofsLZ4Cluster = 4096

// ErofsCompactLZ4Extents reads the documented compact indexes for normal,
// one-block LZ4 pclusters. Other profiles stay opaque, including large,
// packed, inline and interlaced clusters.
func ErofsCompactLZ4Extents(src io.ReaderAt, imageBytes int64, image *ErImage, file *ErFile) ([]ErExtent, error) {
	errProfile := fmt.Errorf("%w: unsupported compact LZ4 geometry", ErrErofs)
	if image.Super.BlkBits != 12 || file.Layout != 3 || file.Size <= 0 || file.Size > 1<<31 ||
		file.Meta.Off < 0 || file.Meta.Len < 32 || file.Meta.Len > 1<<20 ||
		file.Meta.Len+8 > imageBytes || file.Meta.Off > imageBytes-file.Meta.Len-8 {
		return nil, errProfile
	}
	// Map header starts at the first eight-byte boundary after inode and xattrs.
	header := (file.Meta.Off + file.Meta.Len + 7) &^ 7
	var fields [8]byte
	if _, err := src.ReadAt(fields[:], header); err != nil {
		return nil, err
	}
	advice := binary.LittleEndian.Uint16(fields[4:6])
	if advice&^1 != 0 || fields[6] != 0 || fields[7] != 0 {
		return nil, errProfile
	}
	base := header + 8
	count := (file.Size + erofsLZ4Cluster - 1) / erofsLZ4Cluster
	first4 := min(count, ((32-base%32)/4)&7)
	two := int64(0)
	if advice&1 != 0 {
		two = (count - first4) &^ 15
	}
	sections := [...]struct{ entries, entryBytes int64 }{{first4, 4}, {two, 2}, {count - first4 - two, 4}}
	var extents []ErExtent
	var lcn int64
	var data [32]byte
	for _, section := range sections {
		words := int(8 / section.entryBytes)
		if section.entryBytes == 2 {
			words = 16
		}
		groupBytes := words * int(section.entryBytes)
		bits := (groupBytes - 4) * 8 / words
		for i := int64(0); i < section.entries; i += int64(words) {
			if base > imageBytes-int64(groupBytes) {
				return nil, fmt.Errorf("%w: compact index crosses image end", ErrErofs)
			}
			window := data[:groupBytes]
			if _, err := src.ReadAt(window, base); err != nil {
				return nil, err
			}
			pblk := int64(binary.LittleEndian.Uint32(window[groupBytes-4:]))
			word := func(index int) (int, int) {
				at := index * bits
				value := binary.LittleEndian.Uint32(window[at/8:]) >> (at % 8)
				return int(value & 4095), int((value >> 12) & 3)
			}
			for slot := 0; slot < words && i+int64(slot) < section.entries; slot++ {
				shift, kind := word(slot)
				if kind != 2 {
					logical := lcn*erofsLZ4Cluster + int64(shift)
					countBack := int64(1)
					for previous := slot - 1; previous >= 0; previous-- {
						skip, previousKind := word(previous)
						if previousKind == 2 {
							previous -= skip
						}
						if previous >= 0 {
							countBack++
						}
					}
					physical := (pblk + countBack) * erofsLZ4Cluster
					if logical >= file.Size || physical <= 0 || physical > imageBytes-erofsLZ4Cluster {
						return nil, fmt.Errorf("%w: compact extent outside its object", ErrErofs)
					}
					if len(extents) > 0 {
						previous := &extents[len(extents)-1]
						if logical <= previous.LOff {
							return nil, fmt.Errorf("%w: overlapping compact logical extents", ErrErofs)
						}
						previous.LLen = logical - previous.LOff
					}
					extents = append(extents, ErExtent{LOff: logical, POff: physical, PLen: erofsLZ4Cluster})
				}
				lcn++
			}
			base += int64(groupBytes)
		}
	}
	if len(extents) == 0 || extents[0].LOff != 0 {
		return nil, fmt.Errorf("%w: compact indexes do not cover file start", ErrErofs)
	}
	last := &extents[len(extents)-1]
	last.LLen = file.Size - last.LOff
	return extents, nil
}
