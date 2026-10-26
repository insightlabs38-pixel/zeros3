//go:build !linux

package prodshadow

import "os"

func blocks(fi os.FileInfo) int64 { return fi.Size() }
