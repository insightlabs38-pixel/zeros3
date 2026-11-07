package main

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// printRSS reports the process high-water mark and heap peak to stderr (research accounting).
func printRSS(label string) {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Fprintf(os.Stderr, "{\"rss\":%q,\"maxrss_kib\":%d,\"heap_sys_mib\":%d,\"total_alloc_mib\":%d}\n", label, ru.Maxrss, ms.HeapSys>>20, ms.TotalAlloc>>20)
}
