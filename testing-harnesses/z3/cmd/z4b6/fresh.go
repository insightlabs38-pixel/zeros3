package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"zeros3-z3/parquetz3"
	"zeros3-z3/sstz3"
)

func init() {
	cmds["pack"] = pack
	cmds["unpack"] = unpack
}

// pack KIND IN DIR: serialize the exact representation of one native file (KIND sst|parquet):
// DIR/recipe.bin plus content-addressed components DIR/c/<id>. The process then exits.
func pack(args []string) error {
	kind, in, dir := args[0], args[1], args[2]
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "c"), 0o755); err != nil {
		return err
	}
	write := func(id [32]byte, b []byte) error {
		return os.WriteFile(filepath.Join(dir, "c", hex.EncodeToString(id[:])), b, 0o644)
	}
	var recipe []byte
	switch kind {
	case "sst":
		recipe, _, _, err = sstz3.Encode(bytes.NewReader(data), int64(len(data)), sstz3.Options{Form: sstz3.FormUKV, Cutter: &sstz3.HashCutter{Target: 8 << 10, Max: 32 << 10}}, func(_ string, id sstz3.ID, b []byte) error { return write(id, b) })
	case "parquet":
		recipe, _, _, err = parquetz3.Encode(bytes.NewReader(data), int64(len(data)), parquetz3.Options{}, func(_ string, id parquetz3.ID, b []byte) error { return write(id, b) })
	default:
		return fmt.Errorf("unknown kind %q", kind)
	}
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "recipe.bin"), recipe, 0o644)
}

// unpack KIND DIR OUT: rebuild the native file from DIR only (no access to the original).
func unpack(args []string) error {
	kind, dir, out := args[0], args[1], args[2]
	recipe, err := os.ReadFile(filepath.Join(dir, "recipe.bin"))
	if err != nil {
		return err
	}
	get := func(id [32]byte) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, "c", hex.EncodeToString(id[:])))
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	switch kind {
	case "sst":
		err = sstz3.Decode(f, recipe, func(id sstz3.ID) ([]byte, error) { return get(id) }, sstz3.Limits{})
	case "parquet":
		err = parquetz3.Decode(f, recipe, func(id parquetz3.ID) ([]byte, error) { return get(id) }, parquetz3.Limits{})
	default:
		err = fmt.Errorf("unknown kind %q", kind)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
