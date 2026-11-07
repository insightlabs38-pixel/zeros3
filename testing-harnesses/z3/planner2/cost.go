package planner2

// Dollars is a ledger priced under one explicit price book. Latency is never
// converted to dollars; it is a constraint (Config.HotAmp64Cap) and a reported metric.
type Dollars struct {
	Storage, ReadIO, Decode, CPU, MigrateIO, Total float64
}

// Price computes operational cost. Storage integrates physical bytes over the
// simulated days and then holds the final state for HorizonMonths.
func (l *Ledger) Price(pb PriceBook) Dollars {
	var d Dollars
	byteMonths := l.ByteDays/30 + float64(l.FinalPhys)*pb.HorizonMonths
	d.Storage = byteMonths * pb.StorageUSDGBMonth / 1e9
	d.ReadIO = float64(l.ServedBytes) * pb.ReadIOUSDGB / 1e9
	d.Decode = l.DecodeSeconds / 3600 * pb.CPUUSDCoreHour
	d.CPU = (l.AnalysisMSSync + l.AnalysisMSBg + l.MigrateMS + l.ProbeMS) / 1000 / 3600 * pb.CPUUSDCoreHour
	d.MigrateIO = float64(l.SourceReadBytes+l.MigrateReadBytes+l.MigrateWriteBytes) * pb.MigrateIOUSDGB / 1e9
	d.Total = d.Storage + d.ReadIO + d.Decode + d.CPU + d.MigrateIO
	return d
}

// ByteValue is the storage dollars of holding one byte for the whole horizon.
func (p PriceBook) ByteValue() float64 { return p.StorageUSDGBMonth * p.HorizonMonths / 1e9 }
