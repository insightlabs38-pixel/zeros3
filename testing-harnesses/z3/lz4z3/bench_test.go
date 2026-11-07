package lz4z3

import (
	"math/rand"
	"testing"
)

func benchData() []byte { return randPlain(rand.New(rand.NewSource(1)), 1<<20) }

func BenchmarkDecode(b *testing.B) {
	p := benchData()
	blk, _ := PredictBlock(PredDefault, p, 1)
	b.SetBytes(int64(len(p)))
	for i := 0; i < b.N; i++ {
		if _, _, err := Decode(blk, len(p), Limits{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReplayPredicted(b *testing.B) {
	p := benchData()
	rc := Recipe{Kind: KindPredicted, Pred: PredDefault, Accel: 1}
	b.SetBytes(int64(len(p)))
	for i := 0; i < b.N; i++ {
		if _, err := rc.Replay(nil, p, Limits{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReplayExplicit(b *testing.B) {
	p := benchData()
	seqs, _ := Predict(PredDefault, p, 1)
	rc := Recipe{Kind: KindExplicit, Seqs: seqs}
	b.SetBytes(int64(len(p)))
	for i := 0; i < b.N; i++ {
		if _, err := rc.Replay(nil, p, Limits{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReplayHC9(b *testing.B) {
	p := benchData()
	rc := Recipe{Kind: KindPredicted, Pred: PredHC, Accel: 9}
	b.SetBytes(int64(len(p)))
	for i := 0; i < b.N; i++ {
		if _, err := rc.Replay(nil, p, Limits{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAnalyzeMissSweep(b *testing.B) { // worst case: no predictor matches (HC level 12 like input)
	p := benchData()
	seqs, _ := Predict(PredDefault, p, 1)
	seqs = append([]Seq{{Lit: 1, Match: 4, Off: 1}}, seqs...) // diverges at the first sequence
	seqs[1].Lit += 0
	b.SetBytes(int64(len(p)))
	for i := 0; i < b.N; i++ {
		Analyze(p, seqs[:1:1], true)
	}
}
