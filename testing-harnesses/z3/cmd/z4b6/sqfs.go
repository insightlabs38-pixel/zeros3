package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"zeros3-z3/b6cost"
	"zeros3-z3/lab"
	"zeros3-z3/sqfsz3"
)

func init() {
	cmds["sqfs"] = sqfsEcon
	cmds["sqfs-pack"] = sqfsPack
	cmds["sqfs-unpack"] = sqfsUnpack
}

// sqfs ROOT FAMILY...: cumulative retained cost of SquashFS-lz4 image histories ROOT/FAMILY/VER/raw/image.sqsh.
func sqfsEcon(args []string) error {
	root := args[0]
	for _, fam := range args[1:] {
		vers, _ := os.ReadDir(filepath.Join(root, fam))
		var names []string
		for _, v := range vers {
			names = append(names, v.Name())
		}
		sort.Strings(names)
		type pol struct {
			name string
			ch   b6cost.Chunker
		}
		pols := []pol{{"cdc16/64/256", b6cost.ProdCDC}, {"cdc1/4/16", b6cost.FineCDC}}
		for _, p := range pols {
			raw, rep := b6cost.NewStore(), b6cost.NewStore()
			var chunkCost, recCost int64
			var line []string
			var t0 time.Time
			var encT time.Duration
			var in int64
			for _, v := range names {
				img, err := os.ReadFile(filepath.Join(root, fam, v, "raw", "image.sqsh"))
				if err != nil {
					return err
				}
				in += int64(len(img))
				ir := raw.Add(p.ch, img)
				t0 = time.Now()
				recipe, plain, st, err := sqfsz3.Encode(img, sqfsz3.Limits{})
				encT += time.Since(t0)
				if err != nil {
					return fmt.Errorf("%s %s: %w", fam, v, err)
				}
				ic := rep.Add(p.ch, plain)
				chunkCost += ic
				rc := rep.Add(b6cost.Whole, recipe) // verbatim regions and recipe are one object, packed
				recCost += rc
				ic += rc
				line = append(line, fmt.Sprintf("%s: raw %.2fM -> rep %.2fM (blocks %d pred %d hyb %d expl %d; recipe %dB)", v, float64(ir)/1e6, float64(ic)/1e6, st.Blocks, st.Predicted, st.Hybrid, st.Explicit, len(recipe)))
			}
			fmt.Printf("%-16s %-13s CDC-of-image %.2fM  exact-lz4-decoded+CDC %.2fM (%.1f%%) = chunks %.2fM + recipe/verbatim %.2fM  analyze %.0f MiB/s\n", fam, p.name, float64(raw.Total())/1e6, float64(rep.Total())/1e6, 100*float64(rep.Total())/float64(raw.Total()), float64(chunkCost)/1e6, float64(recCost)/1e6, float64(in)/(1<<20)/encT.Seconds())
			for _, l := range line {
				fmt.Println("   ", l)
			}
		}
	}
	return nil
}

var chunker = b6cost.ProdCDC

// sqfs-pack IMAGE DIR: write the serialized representation (recipe + content-addressed chunks).
func sqfsPack(args []string) error {
	img, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	recipe, plain, _, err := sqfsz3.Encode(img, sqfsz3.Limits{})
	if err != nil {
		return err
	}
	dir := args[1]
	if err := os.MkdirAll(filepath.Join(dir, "chunks"), 0o755); err != nil {
		return err
	}
	var order []byte
	for _, c := range lab.ChunkScaled(plain, chunker.Min, chunker.Tgt, chunker.Max) {
		h := sha256.Sum256(c)
		order = append(order, h[:]...)
		if err := os.WriteFile(filepath.Join(dir, "chunks", hex.EncodeToString(h[:])), c, 0o644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "recipe.bin"), recipe, 0o644); err != nil {
		return err
	}
	h := sha256.Sum256(img)
	meta := binary.LittleEndian.AppendUint64(nil, uint64(len(img)))
	meta = append(meta, h[:]...)
	meta = append(meta, order...)
	return os.WriteFile(filepath.Join(dir, "chunks.idx"), meta, 0o644)
}

// sqfs-unpack DIR OUT: fresh-process reconstruction from the serialized representation only.
func sqfsUnpack(args []string) error {
	dir := args[0]
	recipe, err := os.ReadFile(filepath.Join(dir, "recipe.bin"))
	if err != nil {
		return err
	}
	idx, err := os.ReadFile(filepath.Join(dir, "chunks.idx"))
	if err != nil {
		return err
	}
	size, want := binary.LittleEndian.Uint64(idx), idx[8:40]
	var plain bytes.Buffer
	for o := 40; o+32 <= len(idx); o += 32 {
		c, err := os.ReadFile(filepath.Join(dir, "chunks", hex.EncodeToString(idx[o:o+32])))
		if err != nil {
			return err
		}
		if sha256.Sum256(c) != [32]byte(idx[o:o+32]) {
			return fmt.Errorf("chunk %x corrupt", idx[o:o+4])
		}
		plain.Write(c)
	}
	var out bytes.Buffer
	if err := sqfsz3.Decode(&out, recipe, plain.Bytes(), sqfsz3.Limits{}); err != nil {
		return err
	}
	h := sha256.Sum256(out.Bytes())
	if uint64(out.Len()) != size || !bytes.Equal(h[:], want) {
		return fmt.Errorf("reconstruction differs: %d bytes, sha %x", out.Len(), h)
	}
	fmt.Printf("exact: %d bytes sha256 %x\n", out.Len(), h)
	return os.WriteFile(args[1], out.Bytes(), 0o644)
}
