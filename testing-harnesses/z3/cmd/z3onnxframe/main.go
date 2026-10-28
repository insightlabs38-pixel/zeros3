package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"zeros3-z3/onnxz3"
)

func main() {
	data, e := os.ReadFile(os.Args[1])
	if e != nil {
		panic(e)
	}
	p, e := onnxz3.Parse(data)
	if e != nil {
		panic(e)
	}
	type tensor struct {
		Name     string
		Type     int
		Dims     []uint64
		Off, Len int64
		SHA      string
	}
	var tensors []tensor
	for _, t := range p.Tensors {
		tensors = append(tensors, tensor{t.Name, t.Type, t.Dims, t.Off, t.Len, fmt.Sprintf("%x", sha256.Sum256(data[t.Off:t.Off+t.Len]))})
	}
	out := struct {
		Tensors          []tensor
		External, Opaque int
	}{tensors, p.External, p.Opaque}
	if e = json.NewEncoder(os.Stdout).Encode(out); e != nil {
		panic(e)
	}
}
