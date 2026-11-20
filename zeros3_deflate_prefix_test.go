package main

import "testing"

func TestRepIRMatchLengthBoundaries(t *testing.T) {
	const length = 258
	var buf [2*length + 5]byte
	for i := range length {
		buf[i+1] = byte(i*31 + i/8)
		buf[i+length+3] = buf[i+1]
	}
	for _, max := range []int{0, 1, 7, 8, 9, 15, 16, 17, 31, 32, 33, 257, 258} {
		q, pos := 1, length+3
		if got := repIRmatchLen(buf[:], q, pos, max); got != max {
			t.Fatalf("complete match of %d bytes returned %d", max, got)
		}
		for i := range max {
			buf[pos+i] ^= 255
			got := repIRmatchLen(buf[:], q, pos, max)
			buf[pos+i] ^= 255
			if got != i {
				t.Fatalf("limit %d and mismatch at %d returned %d", max, i, got)
			}
		}
	}
	clear(buf[:])
	buf[65] = 1
	if got := repIRmatchLen(buf[:], 0, 1, 258); got != 64 {
		t.Fatalf("overlapping match returned %d, want 64", got)
	}
}
