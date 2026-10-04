package core

import (
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"strings"
)

func crc32B64(b []byte) string {
	var s [4]byte
	binary.BigEndian.PutUint32(s[:], crc32.ChecksumIEEE(b))
	return base64.StdEncoding.EncodeToString(s[:])
}

func unquote(etag string) string { return strings.Trim(etag, `"`) }

func lowerKeys(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}
