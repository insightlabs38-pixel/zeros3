package nativerescue

import (
	"zeros3-z3/econx"
	"zeros3-z3/lab"
)

// Structure uses the existing exact format partition; no new executable parser.
func Structure(data []byte) (*lab.Plan, error) {
	b, e := loadBin("native", data)
	if e != nil {
		return nil, e
	}
	var cuts []int
	for _, r := range b.Im.Regions {
		cuts = append(cuts, r.Off+r.Len)
	}
	return econx.ConcatPlan(data, cuts, "native-raw-section"), nil
}
