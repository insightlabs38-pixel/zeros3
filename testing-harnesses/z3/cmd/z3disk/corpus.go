package main

import "zeros3-z3/diskimg"

const (
	MiB     = diskimg.MiB
	KiB     = diskimg.KiB
	capSize = 4 * diskimg.GiB
	seed0   = 26
)

// Rev is a named logical disk state.
type Rev struct {
	Name   string
	Disk   *diskimg.Disk
	Parent string // Family B: name of the parent state ("" = standalone)
	Note   string
}

func w(d *diskimg.Disk, off, ln int64, k diskimg.Kind, seed uint64) {
	d.Writes = append(d.Writes, diskimg.Extent{Off: off, Len: ln, Kind: k, Seed: seed})
}

// familyA is the cumulative rebuilt-standalone chain R0..R7, the rewrite-equivalent control R8 (= R7's logical disk,
// built through a different write history) and the mostly-unrelated R9.
func familyA() []Rev {
	r0 := diskimg.BaseDisk(capSize, seed0)
	var out []Rev
	add := func(name, note string, d *diskimg.Disk) { out = append(out, Rev{Name: name, Disk: d, Note: note}) }
	add("R0", "base", r0)
	r1 := r0.Clone()
	w(r1, diskimg.OffRand+5*MiB+3*4096, 4096, diskimg.KRandom, 101)
	add("R1", "4 KiB edit inside allocated random extent", r1)
	r2 := r1.Clone()
	w(r2, diskimg.OffHole+100*MiB+7*4096, 4096, diskimg.KRandom, 102)
	add("R2", "4 KiB write into a hole", r2)
	r3 := r2.Clone()
	w(r3, diskimg.OffText+8*MiB, MiB, diskimg.KZero, 0)
	add("R3", "zero 1 MiB of compressible data", r3)
	r4 := r3.Clone()
	w(r4, diskimg.OffRand+20*MiB+2*4096, MiB, diskimg.KRandom, 104)
	add("R4", "1 MiB contiguous mutation (not cluster aligned)", r4)
	r5 := r4.Clone()
	for i := uint64(0); i < 128; i++ {
		pg := int64(mixSeed(500+i) % uint64(capSize/4096))
		w(r5, pg*4096, 4096, diskimg.KRandom, 600+i)
	}
	add("R5", "128 scattered 4 KiB writes over the whole disk", r5)
	r6 := r5.Clone()
	w(r6, diskimg.OffDup+37*MiB+100*KiB, 64*KiB, diskimg.KRandom, 106)
	add("R6", "64 KiB change inside one duplicate-region instance", r6)
	r7 := r6.Clone()
	w(r7, diskimg.OffDup+128*MiB-4096, 8192, diskimg.KRandom, 107)
	add("R7", "8 KiB write straddling a 2 MiB / 64 KiB boundary at the end of a region", r7)
	add("R8", "control: R7's logical disk, data written from high offsets down", r7.Clone())
	add("R8b", "control: R7's logical disk, data written in shuffled 2 MiB groups", r7.Clone())
	add("R9", "mostly unrelated: same layout, new seeds", diskimg.BaseDisk(capSize, 900))
	return out
}

func mixSeed(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// familyB is base B0, six independent children C1..C6 of B0, and grandchild C7 (child of C6).
func familyB() []Rev {
	b0 := diskimg.BaseDisk(capSize, seed0)
	out := []Rev{{Name: "B0", Disk: b0, Note: "base"}}
	child := func(name, parent, note string, par *diskimg.Disk, f func(d *diskimg.Disk)) *diskimg.Disk {
		d := par.Clone()
		f(d)
		out = append(out, Rev{Name: name, Disk: d, Parent: parent, Note: note})
		return d
	}
	child("C1", "B0", "single 4 KiB change in an inherited region", b0, func(d *diskimg.Disk) {
		w(d, diskimg.OffStruct+33*MiB+5*4096, 4096, diskimg.KRandom, 201)
	})
	child("C2", "B0", "six widely separated 4 KiB changes", b0, func(d *diskimg.Disk) {
		for i, off := range []int64{diskimg.OffMeta + 3*MiB, diskimg.OffText + 30*MiB, diskimg.OffRand + 50*MiB, diskimg.OffDup + 90*MiB, diskimg.OffStruct + 70*MiB, diskimg.OffScat + 100*MiB} {
			w(d, off, 4096, diskimg.KRandom, 210+uint64(i))
		}
	})
	child("C3", "B0", "explicit zero override: 1 MiB (aligned) + 16 KiB (partial cluster/block) over nonzero parent data", b0, func(d *diskimg.Disk) {
		w(d, diskimg.OffRand+4*MiB, MiB, diskimg.KZero, 0)
		w(d, diskimg.OffRand+30*MiB+5*4096, 16*KiB, diskimg.KZero, 0)
	})
	child("C4", "B0", "write into a parent hole and into a parent explicit-zero range", b0, func(d *diskimg.Disk) {
		w(d, diskimg.OffHole+200*MiB, 4096, diskimg.KRandom, 230)
		w(d, diskimg.OffZeroR+10*MiB+4096, 4096, diskimg.KRandom, 231)
	})
	child("C5", "B0", "three 4 KiB sectors inside one 2 MiB block (partially present block)", b0, func(d *diskimg.Disk) {
		for i, s := range []int64{0, 17, 100} {
			w(d, diskimg.OffRand+60*MiB+s*4096, 4096, diskimg.KRandom, 240+uint64(i))
		}
	})
	c6 := child("C6", "B0", "4 MiB changed extent", b0, func(d *diskimg.Disk) {
		w(d, diskimg.OffRand+44*MiB+4*4096, 4*MiB, diskimg.KRandom, 250)
	})
	child("C7", "C6", "grandchild: edits inherited-from-base data, C6-modified data, and zeroes 64 KiB of C6 data", c6, func(d *diskimg.Disk) {
		w(d, diskimg.OffText+20*MiB, 4096, diskimg.KRandom, 260)
		w(d, diskimg.OffRand+44*MiB+2*MiB, 4096, diskimg.KRandom, 261)
		w(d, diskimg.OffRand+46*MiB, 64*KiB, diskimg.KZero, 0)
	})
	return out
}
