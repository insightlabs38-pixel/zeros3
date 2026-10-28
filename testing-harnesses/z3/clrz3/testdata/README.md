Three 7.5 KB assemblies built by the .NET SDK 8.0.427 C# compiler (Roslyn), `net8.0`, `Release`,
`Deterministic`, `ContinuousIntegrationBuild`, `DebugType=none`, `AnyCPU`, from the Z3-33
producer family generator (`z3clr gen DIR family n=6 r0 body earlytype`):

| file | mutation of r0 |
|---|---|
| fixture-r0.dll | baseline, 6 classes of 10 methods |
| fixture-body.dll | one method body changed |
| fixture-earlytype.dll | a new type that sorts first (shifts every table row) |

PRODUCER_WORKLOAD; the sources are produced by `cmd/z3clr/gen.go`.
