package planner2

import "fmt"

// Base parameter set shared by presets; grid values come from the preregistered protocol.
func baseParams(name string) AParams {
	return AParams{Name: name, PremiumCap: 0.25, K: 4, MinConf: 0.3, Horizon: 2, PUTAnalyzeMS: 1e12, ColdAge: 14,
		MaxMoves: 6, QueuePerTick: 6, Margin: 0.0005}
}

// Preset returns a policy by name. Simple baselines are the Z3-36 ports.
func Preset(name string, pr Prior) (Policy, error) {
	switch name {
	case "always-cdc", "greedy", "premium-cap", "late-bootstrap", "expected-value":
		return NewZ3(name), nil
	case "premium-cap-0":
		p := baseParams(name)
		p.PremiumCap = 0
		return NewAdaptive(p, pr), nil
	}
	p := baseParams(name)
	switch name {
	case "sim-conf": // similarity-confidence admission with calibrated static priors
		p.Conf = true
	case "lineage-prior":
		p.Lineage = true
	case "class-prior":
		p.Class, p.Lineage = true, false
	case "lifetime":
		p.Class, p.Lineage, p.Life = true, true, true
	case "read-ev":
		p.Class, p.Lineage, p.Read = true, true, true
	case "bandit":
		p.Class, p.Lineage, p.Bandit = true, true, true
	case "deferred":
		p.Class, p.Lineage, p.Defer, p.PUTAnalyzeMS, p.Conf = true, true, true, 200, true
	case "group-reopt":
		p.Class, p.Lineage, p.Conf, p.Group, p.Defer, p.PUTAnalyzeMS = true, true, true, true, true, 200
	case "combined":
		p.Class, p.Lineage, p.Conf, p.Life, p.Read, p.Group, p.Defer, p.PUTAnalyzeMS = true, true, true, true, true, true, true, 200
	default:
		return nil, fmt.Errorf("planner2: unknown policy %q", name)
	}
	return NewAdaptive(p, pr), nil
}

// Policies lists every policy in the study, simple baselines first.
var PolicyNames = []string{"always-cdc", "greedy", "premium-cap", "late-bootstrap", "expected-value",
	"sim-conf", "lineage-prior", "class-prior", "lifetime", "read-ev", "bandit", "deferred", "group-reopt", "combined"}
