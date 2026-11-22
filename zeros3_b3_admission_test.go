package main

// B3 admission probe: ZEROS3_B3_TGZ=a.tgz,b.tgz,c.tgz go test -run TestB3AdmissionProbe -v .
// It compiles against the pre-B3 tree too, so old and new pricing run the same cohort.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestB3AdmissionProbe(t *testing.T) {
	list := os.Getenv("ZEROS3_B3_TGZ")
	if list == "" {
		t.Skip("set ZEROS3_B3_TGZ")
	}
	var files [][]byte
	var logical int64
	for _, p := range strings.Split(list, ",") {
		b, err := os.ReadFile(p)
		mustOK(t, err)
		files, logical = append(files, b), logical+int64(len(b))
	}
	lab := func() *authLab {
		l := newAuthLab(t)
		for i, b := range files {
			l.put(fmt.Sprintf("v%d.tgz", i), b)
		}
		return l
	}
	out := map[string]any{"logical": logical}
	cdc := lab()
	cdc.offline(func() {
		_, err := repackStore(cdc.dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: true, Recompress: true})
		mustOK(t, err)
	})
	out["cdc_physical"] = dirBytes(t, cdc.dir)

	l := lab()
	sum := func(rep AuthReport) (added, cdcBytes int64, forms []string) {
		for _, p := range rep.Plans {
			added, cdcBytes, forms = added+p.AddedBytes, cdcBytes+p.CDCBytes, append(forms, p.Form)
		}
		return
	}
	ordinary, err := l.s.AuthorityMigrate(AuthPolicy{Order: "oldest", AllowRep: true, CodecReplay: true, NoZN7: true, MinSaving: 0.10})
	mustOK(t, err)
	a, c, f := sum(ordinary)
	out["ordinary_forms"], out["ordinary_added"], out["ordinary_cdc"] = f, a, c
	reasons := []string{}
	for _, p := range ordinary.Plans {
		reasons = append(reasons, p.Reason)
	}
	out["ordinary_reasons"] = reasons
	forcedPlan, err := l.s.AuthorityMigrate(AuthPolicy{Order: "oldest", AllowRep: true, CodecReplay: true, NoZN7: true, MinSaving: -100})
	mustOK(t, err)
	fa, fc, _ := sum(forcedPlan)
	out["forced_plan_added"], out["forced_plan_cdc"] = fa, fc
	if fc > 0 {
		out["forced_plan_saving"] = 1 - float64(fa)/float64(fc)
	}
	applied := lab()
	applied.migrate(AuthPolicy{Strategy: "forced", AllowRep: true, MinSaving: -100, NoZN7: true, CodecReplay: true})
	applied.offline(func() {
		_, err := gcCollect(applied.dir, true)
		mustOK(t, err)
		_, err = repackStore(applied.dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: true, Recompress: true})
		mustOK(t, err)
		_, err = compactStore(applied.dir, authCompactOpts())
		mustOK(t, err)
	})
	out["forced_physical"] = dirBytes(t, applied.dir)
	out["actual_saving"] = 1 - float64(out["forced_physical"].(int64))/float64(out["cdc_physical"].(int64))
	line, _ := json.Marshal(out)
	fmt.Printf("B3ADMIT %s\n", line)
}
