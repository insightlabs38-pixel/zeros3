package planner2

import (
	"fmt"
	"math"
)

// apply executes maintenance actions with full charging and fail-closed checks.
func (r *runner) apply(env *Env, acts []Action) {
	for _, a := range acts {
		switch a.Kind {
		case ActEnqueue:
			r.enqueue(a)
		case ActMigrate, ActFlatten, ActRevert:
			r.migrate(env, a)
		case ActReopt:
			r.reopt(env, a)
		case ActDropSide:
			r.dropSide(a)
		default:
			r.led.RejectedInvalid++
		}
	}
}

func (r *runner) dropSide(a Action) {
	o, ok := r.st.Obj(a.Obj)
	if !ok || !o.Live || !o.Attached || o.Side == nil {
		r.led.RejectedInvalid++
		return
	}
	if err := r.st.Detach(*o.Side); err != nil {
		r.led.RejectedInvalid++
		return
	}
	o.Side = nil
	r.log(DecisionRec{Seq: r.seq, Day: r.day, Obj: a.Obj, Kind: "dropside", Reason: a.Reason})
}

// migrate moves (or adds a sidecar for) a stored object. The new closure is written
// first (the migration peak), then the old one is released; failures leave garbage.
func (r *runner) migrate(env *Env, a Action) {
	o, ok := r.st.Obj(a.Obj)
	if !ok || !o.Live || !o.Attached {
		r.led.RejectedInvalid++
		return
	}
	scheme, form := a.Scheme, a.Form
	switch a.Kind {
	case ActRevert:
		scheme, form = CDC, ""
	case ActFlatten:
		scheme, form = o.Primary.Scheme, "std"
	}
	if scheme == "" {
		r.led.RejectedInvalid++
		return
	}
	target, err := r.pick(env, a.Obj, scheme, form)
	if err != nil {
		r.countReject(err)
		return
	}
	if !a.Sidecar && target.Scheme == o.Primary.Scheme && target.Form == o.Primary.Form {
		return
	}
	marg := r.st.Marginal(target)
	cpu := int64(math.Ceil(target.AnalysisMS))
	if target.Scheme == CDC {
		cpu = 0
	}
	io := o.Size + marg // read the source, write the new components
	if r.inTick {
		if r.win.CPUMS < cpu || r.win.IOBytes < io {
			r.led.RejectedBudget++
			return
		}
	}
	// speculative staging (B2 'best-of-two' migrations): extra transient bytes above the final write
	spec := int64(0)
	if r.cfg.SpecFactor > 1 {
		spec = int64(float64(marg) * (r.cfg.SpecFactor - 1))
		if r.cfg.Capacity > 0 && r.st.Phys()+marg+spec > r.cfg.Capacity {
			r.led.RejectedSpace++
			return
		}
	}
	// failure injection: deterministic per (seed, object, attempt)
	r.migTry++
	if r.cfg.FailMigrate > 0 && hash01(r.cfg.Seed, uint64(a.Obj), uint64(r.migTry), 77) < r.cfg.FailMigrate {
		if _, _, err := r.st.Attach(a.Obj, target); err == nil {
			_ = r.st.Detach(target) // abort: written components become dead garbage until GC
			r.led.WriteBytes += marg
			r.led.MigrateWriteBytes += marg
			r.led.MigrateReadBytes += o.Size
		}
		r.led.FailedMigrations++
		r.consume(cpu, io)
		r.track()
		r.log(DecisionRec{Seq: r.seq, Day: r.day, Obj: a.Obj, Kind: "migrate-failed", Scheme: scheme, Form: target.Form, Marginal: marg})
		return
	}
	if _, _, err := r.st.Attach(a.Obj, target); err != nil {
		r.led.RejectedSpace++
		return
	}
	r.track()
	if spec > 0 && r.st.Phys()+spec > r.led.Peak {
		r.led.Peak = r.st.Phys() + spec
	}
	if a.Sidecar && target.Scheme != CDC {
		if o.Side != nil {
			_ = r.st.Detach(*o.Side)
		}
		t := target
		o.Side = &t
		r.led.Sidecars++
	} else {
		old := o.Primary
		if err := r.st.Detach(old); err != nil {
			panic(err) // invariant: attached objects always hold their closure
		}
		if o.Side != nil {
			_ = r.st.Detach(*o.Side)
			o.Side = nil
		}
		o.Primary = target
		if target.Scheme == CDC {
			r.led.Reverts++
		}
		if a.Kind == ActFlatten {
			r.led.Rebases++
		}
	}
	r.led.Migrations++
	r.led.WriteBytes += marg
	r.led.MigrateWriteBytes += marg
	r.led.MigrateReadBytes += o.Size
	r.led.MigrateMS += float64(cpu)
	r.consume(cpu, io)
	r.log(DecisionRec{Seq: r.seq, Day: r.day, Obj: a.Obj, Kind: "migrate", Scheme: scheme, Form: target.Form, Marginal: marg, Sidecar: a.Sidecar, Reason: a.Reason})
}

func (r *runner) consume(cpu, io int64) {
	if r.inTick {
		r.win.CPUMS -= cpu
		r.win.IOBytes -= io
	}
}

// reopt applies the lineage's measured group reoptimisation (B3 optimize): every
// stored object of the action's scheme is re-expressed; new components are all
// written before any old one is released, so the peak is before+after.
func (r *runner) reopt(env *Env, a Action) {
	l, _, err := r.locate(a.Obj)
	if err != nil {
		r.led.RejectedInvalid++
		return
	}
	acts := l.GroupActions()
	if len(acts) == 0 {
		r.led.RejectedInvalid++
		return
	}
	ga := acts[0]
	li := r.objLin[a.Obj]
	type rewrite struct {
		o   *Obj
		neu Cand
	}
	var rs []rewrite
	var io int64
	for t := 0; t < l.Len(); t++ {
		id, ok := r.stageObj[[2]int{li, t}]
		if !ok {
			continue
		}
		o, ok := r.st.Obj(id)
		if !ok || !o.Live || !o.Attached || o.Primary.Scheme != ga.Scheme || len(o.Primary.Comps) == 0 {
			continue
		}
		if o.Primary.Form == "opt" {
			continue // already reoptimised
		}
		f := float64(ga.AfterTotal) / float64(ga.BeforeTotal)
		n := o.Primary
		n.Form = "opt"
		n.Base = -1
		n.Depth = 1
		n.Comps = []CompRef{{ID: CompID(fmt.Sprintf("%s|%s|%d|opt", l.Name(), ga.Scheme, t)), Bytes: int64(float64(o.Primary.Comps[0].Bytes) * f)}}
		n.Physical = "modeled"
		rs = append(rs, rewrite{o, n})
		io += o.Size
	}
	if len(rs) == 0 {
		return
	}
	cpu := int64(ga.CPUMS)
	if r.inTick && (r.win.CPUMS < cpu || r.win.IOBytes < io) {
		r.led.RejectedBudget++
		return
	}
	var added []Cand
	for _, x := range rs {
		if _, _, err := r.st.Attach(x.o.ID, x.neu); err != nil {
			for _, c := range added {
				_ = r.st.Detach(c)
			}
			r.led.RejectedSpace++
			return
		}
		added = append(added, x.neu)
	}
	r.track()
	for _, x := range rs {
		_ = r.st.Detach(x.o.Primary)
		x.o.Primary = x.neu
	}
	r.led.Reopts++
	r.led.MigrateMS += ga.CPUMS
	r.led.MigrateReadBytes += io
	r.consume(cpu, io)
	r.log(DecisionRec{Seq: r.seq, Day: r.day, Obj: a.Obj, Kind: "reopt", Scheme: ga.Scheme, Reason: a.Reason})
}
