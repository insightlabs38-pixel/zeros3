package graphnorm

import "sort"

var _ = sort.Ints

// Match bases, strongest first. Only BasisExact is a structural proof (equal identity
// hash, unique on both sides); all others are contextual and flagged so callers can price
// or reject them. Nothing here is ever required for exactness: reconstruction verifies bytes.
const (
	BasisNone     uint8 = iota
	BasisExact          // unique identical identity in both graphs
	BasisDupOrder       // identical duplicates paired by occurrence order (interchangeable only if contexts agree)
	BasisContext        // unique best candidate by matched-neighborhood agreement, zero conflicts
	BasisAssign         // optimal bounded bipartite assignment inside an ambiguous partition
)

type MatchResult struct {
	Map     []int32 // a -> b, -1 unmatched
	Basis   []uint8
	Count   [5]int
	Reject  int // candidates rejected for ambiguity or conflict
	Passes  int
	Assigns int
}

type MatchOptions struct {
	MaxPasses   int
	AssignLimit int  // max partition size for exact bipartite assignment (0 = disabled)
	DupByOrder  bool // pair equal-size duplicate classes by occurrence order
}

func DefaultMatch() MatchOptions {
	return MatchOptions{MaxPasses: 64, AssignLimit: 24, DupByOrder: true}
}

type radj struct {
	from int32
	pos  int32
	kind uint16
}

// maxAssign finds a maximum-weight assignment (entries < 0 forbidden) by Hungarian-style
// DP over the smaller side's subsets is exponential; instead use the O(n^3) Kuhn-Munkres on a
// padded square matrix, and report uniqueness by re-solving with each chosen edge forbidden.
func maxAssign(w [][]int) ([]int, bool) {
	n := max(len(w), len(w[0]))
	const forb = -1 << 20
	c := make([][]int, n)
	for i := range c {
		c[i] = make([]int, n)
		for j := range c[i] {
			v := 0
			if i < len(w) && j < len(w[0]) {
				v = w[i][j]
				if v < 0 {
					v = forb
				}
			}
			c[i][j] = -v // minimize cost
		}
	}
	solve := func() ([]int, int) {
		inf := 1 << 40
		u := make([]int, n+1)
		v := make([]int, n+1)
		p := make([]int, n+1)
		way := make([]int, n+1)
		for i := 1; i <= n; i++ {
			p[0] = i
			j0 := 0
			minv := make([]int, n+1)
			used := make([]bool, n+1)
			for j := range minv {
				minv[j] = inf
			}
			for {
				used[j0] = true
				i0, delta, j1 := p[j0], inf, 0
				for j := 1; j <= n; j++ {
					if !used[j] {
						cur := c[i0-1][j-1] - u[i0] - v[j]
						if cur < minv[j] {
							minv[j], way[j] = cur, j0
						}
						if minv[j] < delta {
							delta, j1 = minv[j], j
						}
					}
				}
				for j := 0; j <= n; j++ {
					if used[j] {
						u[p[j]] += delta
						v[j] -= delta
					} else {
						minv[j] -= delta
					}
				}
				j0 = j1
				if p[j0] == 0 {
					break
				}
			}
			for {
				j1 := way[j0]
				p[j0] = p[j1]
				j0 = j1
				if j0 == 0 {
					break
				}
			}
		}
		asg := make([]int, n)
		cost := 0
		for j := 1; j <= n; j++ {
			asg[p[j]-1] = j - 1
			cost += c[p[j]-1][j-1]
		}
		return asg, cost
	}
	asg, best := solve()
	// uniqueness: forbidding any positive chosen edge must strictly worsen the optimum
	unique := true
	for i := 0; i < len(w) && unique; i++ {
		j := asg[i]
		if j >= len(w[0]) || w[i][j] <= 0 {
			continue
		}
		old := c[i][j]
		c[i][j] = -forb
		_, alt := solve()
		c[i][j] = old
		if alt <= best {
			unique = false
		}
	}
	out := make([]int, len(w))
	for i := range out {
		out[i] = -1
		if asg[i] < len(w[0]) {
			out[i] = asg[i]
		}
	}
	return out, unique
}
