package lzl

const (
	numStates   = 12
	pb          = 2 // position bits for isMatch / length contexts
	posStates   = 1 << pb
	lc          = 3 // literal context: high lc bits of the previous byte
	minMatch    = 2
	maxMatch    = 273
	numReps     = 4
	lenLowBits  = 3
	lenMidBits  = 3
	lenHighBits = 8
	lenStates   = 4 // distance contexts by length
	slotBits    = 6
	endPosModel = 14 // slots below this use context-coded footers
	alignBits   = 4
)

func stateLit(s int) int {
	switch {
	case s < 4:
		return 0
	case s < 10:
		return s - 3
	}
	return s - 6
}
func stateMatch(s int) int {
	if s < 7 {
		return 7
	}
	return 10
}
func stateRep(s int) int {
	if s < 7 {
		return 8
	}
	return 11
}
func stateShortRep(s int) int {
	if s < 7 {
		return 9
	}
	return 11
}

type lenModel struct {
	choice, choice2 prob
	low             [posStates][1 << lenLowBits]prob
	mid             [posStates][1 << lenMidBits]prob
	high            [1 << lenHighBits]prob
}

// model holds every adaptive probability. It is reset per frame.
type model struct {
	isMatch    [numStates][posStates]prob
	isRep      [numStates]prob
	isRepG0    [numStates]prob
	isRepG1    [numStates]prob
	isRepG2    [numStates]prob
	isRep0Long [numStates][posStates]prob
	lit        [1 << lc][0x300]prob
	slot       [lenStates][1 << slotBits]prob
	spec       [endPosModel][1 << 6]prob // footers of slots 4..13 (at most 5 bits)
	align      [1 << alignBits]prob
	matchLen   lenModel
	repLen     lenModel
}

func (m *model) reset() {
	fill := func(p []prob) {
		for i := range p {
			p[i] = probInit
		}
	}
	for s := range numStates {
		fill(m.isMatch[s][:])
		fill(m.isRep0Long[s][:])
	}
	fill(m.isRep[:])
	fill(m.isRepG0[:])
	fill(m.isRepG1[:])
	fill(m.isRepG2[:])
	for i := range m.lit {
		fill(m.lit[i][:])
	}
	for i := range m.slot {
		fill(m.slot[i][:])
	}
	for i := range m.spec {
		fill(m.spec[i][:])
	}
	fill(m.align[:])
	for _, l := range []*lenModel{&m.matchLen, &m.repLen} {
		l.choice, l.choice2 = probInit, probInit
		for p := range posStates {
			fill(l.low[p][:])
			fill(l.mid[p][:])
		}
		fill(l.high[:])
	}
}

func litCtx(prev byte) int { return int(prev >> (8 - lc)) }

// distSlot returns LZMA's slot for d = distance-1.
func distSlot(d uint32) uint32 {
	if d < 4 {
		return d
	}
	n := uint32(31)
	for d>>n == 0 {
		n--
	}
	return 2*n + (d>>(n-1))&1
}

func lenState(l int) int { return min(l-minMatch, lenStates-1) }
