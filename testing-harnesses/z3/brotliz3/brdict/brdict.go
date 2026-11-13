// Package brdict links the RFC 7932 static dictionary into a program and installs it in brotliz3.
package brdict

import (
	_ "embed"

	"zeros3-z3/brotliz3"
)

//go:embed dictionary.bin
var data []byte

func init() {
	if err := brotliz3.SetDictionary(data); err != nil {
		panic(err)
	}
}
