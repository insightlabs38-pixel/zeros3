package debugsym

import (
	"bytes"
	"debug/dwarf"
	"debug/elf"
	"fmt"
	"strings"
)

// DWARFSnapshot is transient attribution evidence. It never feeds reconstruction
// or claims byte-level entropy attribution from semantic attribute counts.
type DWARFSnapshot struct {
	VersionSections []string
	Entries         int
	Named           map[string]map[string]string
}

func SnapshotDWARF(data []byte) (*DWARFSnapshot, error) {
	if int64(len(data)) > MaxFile {
		return nil, bad("DWARF file bound")
	}
	f, e := elf.NewFile(bytes.NewReader(data))
	if e != nil {
		return nil, e
	}
	defer f.Close()
	out := &DWARFSnapshot{Named: map[string]map[string]string{}}
	var total uint64
	for _, s := range f.Sections {
		if strings.HasPrefix(s.Name, ".debug") || strings.HasPrefix(s.Name, ".zdebug") {
			if s.Flags&elf.SHF_COMPRESSED != 0 || strings.HasPrefix(s.Name, ".zdebug") {
				return nil, bad("compressed DWARF kept opaque for attribution")
			}
			total += s.Size
			if s.Size > MaxDirectory || total > 64<<20 {
				return nil, bad("DWARF section budget")
			}
			out.VersionSections = append(out.VersionSections, s.Name)
		}
	}
	d, e := f.DWARF()
	if e != nil {
		return nil, e
	}
	r := d.Reader()
	var parents []string
	offsets := map[dwarf.Offset]string{}
	var records []struct {
		key   string
		entry *dwarf.Entry
	}
	for {
		entry, e := r.Next()
		if e != nil {
			return nil, e
		}
		if entry == nil {
			break
		}
		if entry.Tag == 0 {
			if len(parents) > 0 {
				parents = parents[:len(parents)-1]
			}
			continue
		}
		out.Entries++
		if out.Entries > 1<<20 {
			return nil, bad("DIE count budget")
		}
		name, _ := entry.Val(dwarf.AttrName).(string)
		parent := ""
		if len(parents) > 0 {
			parent = parents[len(parents)-1]
		}
		key := parent
		if name != "" {
			key = parent + "/" + entry.Tag.String() + ":" + name
			if len(key) > 8192 {
				return nil, bad("DIE path budget")
			}
			offsets[entry.Offset] = key
			records = append(records, struct {
				key   string
				entry *dwarf.Entry
			}{key, entry})
		}
		if entry.Children {
			parents = append(parents, key)
			if len(parents) > 256 {
				return nil, bad("DIE depth budget")
			}
		}
	}
	for _, rec := range records {
		if _, exists := out.Named[rec.key]; exists {
			continue
		}
		attrs := map[string]string{}
		for _, v := range rec.entry.Field {
			switch v.Attr {
			case dwarf.AttrLowpc, dwarf.AttrHighpc, dwarf.AttrEntrypc, dwarf.AttrRanges, dwarf.AttrLocation, dwarf.AttrFrameBase, dwarf.AttrDeclLine, dwarf.AttrDeclColumn, dwarf.AttrType:
				value := fmt.Sprint(v.Val)
				if off, ok := v.Val.(dwarf.Offset); ok {
					if name, ok := offsets[off]; ok {
						value = name
					}
				}
				attrs[v.Attr.String()] = value
			}
		}
		out.Named[rec.key] = attrs
	}
	return out, nil
}
