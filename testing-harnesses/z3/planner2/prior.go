package planner2

import "math"

// Calibrate estimates pooled priors from earlier (training) lineages only. It reads
// the complete captured histories, so it must never be given a held-out fold.
func Calibrate(lins []Lineage) Prior {
	var logRatio, ratioN float64
	var rhoRep, rhoRepN, rhoC, rhoCN float64
	var cont, trials float64
	for _, l := range lins {
		n := l.Len()
		cont += float64(n - 1)
		trials += float64(n)
		for _, si := range l.Schemes() {
			if !si.Ready {
				continue
			}
			for t := 0; t < n; t++ {
				fc := l.Forms(0, t, CDC, nil)
				fs := l.Forms(0, t, si.Name, nil)
				if len(fc) == 0 || len(fs) == 0 {
					continue
				}
				if si.Name != CDC {
					c, s := float64(fc[0].OwnBytes()), float64(fs[0].OwnBytes())
					if c > 0 && s > 0 {
						logRatio += math.Log(s / c)
						ratioN++
					}
				}
				if t == 0 {
					continue
				}
				var r float64
				if l.Layer() == "B" {
					r = l.Reuse(t, si.Name)
				} else {
					r = Overlap(l.Sketch(t-1, si.Name), l.Sketch(t, si.Name))
				}
				if si.Name == CDC {
					rhoC += r
					rhoCN++
				} else {
					rhoRep += r
					rhoRepN++
				}
			}
		}
	}
	p := DefaultPrior()
	if ratioN > 0 {
		p.Ratio = math.Exp(logRatio / ratioN)
	}
	if rhoRepN > 0 {
		p.RhoRep = rhoRep / rhoRepN
	}
	if rhoCN > 0 {
		p.RhoCDC = rhoC / rhoCN
	}
	if trials > 0 {
		p.Q = math.Min(0.95, math.Max(0.05, cont/trials))
	}
	p.N = len(lins)
	return p
}
