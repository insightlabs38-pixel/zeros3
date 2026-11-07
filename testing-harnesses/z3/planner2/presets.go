package planner2

import "fmt"

// Base parameter set shared by presets; grid values come from the preregistered protocol.
func baseParams(name string) AParams {
	return AParams{Name: name, PremiumCap: 0.25, K: 4, MinConf: 0.3, Horizon: 2, PUTAnalyzeMS: 1e12, ColdAge: 14,
		MaxMoves: 6, QueuePerTick: 6, Margin: 0.0005}
}

// Preset returns a policy by name. Simple baselines are the Z3-36 ports.
func Preset(name string, pr Prior) (Policy, error) { return PresetWith(name, pr, nil) }

// PresetWith applies tuned numeric parameters (grid values) on top of a preset.
func PresetWith(name string, pr Prior, tune *AParams) (Policy, error) {
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
	case "cons": // never pays a first-sight premium: the lock-in baseline for group repair
		p.Class, p.Lineage, p.Conf, p.PremiumCap, p.FixedCap = true, true, true, 0, true
	case "cons-group": // same, plus retrospective group bootstrap
		p.Class, p.Lineage, p.Conf, p.PremiumCap, p.FixedCap = true, true, true, 0, true
		p.Group, p.Defer, p.PUTAnalyzeMS = true, true, 1e12
	case "deferred":
		p.Class, p.Lineage, p.Defer, p.PUTAnalyzeMS, p.Conf = true, true, true, 200, true
	case "group-reopt":
		p.Class, p.Lineage, p.Conf, p.Group, p.Defer, p.PUTAnalyzeMS = true, true, true, true, true, 200
	case "combined":
		combined(&p)
	case "combined-no-group", "combined-no-defer", "combined-no-read", "combined-no-life", "combined-no-conf", "combined-no-lineage", "combined-no-class":
		combined(&p)
		switch name {
		case "combined-no-group":
			p.Group = false
		case "combined-no-defer":
			p.Defer, p.PUTAnalyzeMS = false, 1e12
		case "combined-no-read":
			p.Read = false
		case "combined-no-life":
			p.Life = false
		case "combined-no-conf":
			p.Conf = false
		case "combined-no-lineage":
			p.Lineage = false
		case "combined-no-class":
			p.Class = false
		}
	default:
		return nil, fmt.Errorf("planner2: unknown policy %q", name)
	}
	if tune != nil {
		if !p.FixedCap {
			p.PremiumCap = tune.PremiumCap
		}
		p.K, p.MinConf, p.Horizon = tune.K, tune.MinConf, tune.Horizon
	}
	return NewAdaptive(p, pr), nil
}

// Policies lists every policy in the study, simple baselines first.
var PolicyNames = []string{"always-cdc", "greedy", "premium-cap", "late-bootstrap", "expected-value",
	"sim-conf", "lineage-prior", "class-prior", "lifetime", "read-ev", "bandit", "deferred", "group-reopt", "combined", "cons", "cons-group"}

func combined(p *AParams) {
	p.Class, p.Lineage, p.Conf, p.Life, p.Read, p.Group, p.Defer, p.PUTAnalyzeMS = true, true, true, true, true, true, true, 200
}

// AblationNames are the one-mechanism-removed variants of the combined policy.
var AblationNames = []string{"combined-no-group", "combined-no-defer", "combined-no-read", "combined-no-life", "combined-no-conf", "combined-no-lineage", "combined-no-class"}
