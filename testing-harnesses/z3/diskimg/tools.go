package diskimg

import (
	"os"
	"os/exec"
)

// ToolPath finds a laboratory tool: $Z3_<ENV> if set, else name on PATH, else fallback.
func ToolPath(env, name, fallback string) string {
	if p := os.Getenv(env); p != "" {
		return p
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return fallback
}

// VhdxGen / VhdiRead locate the isolated producer and the independent reader oracle (built outside Git).
func VhdxGen() string {
	return ToolPath("Z3_VHDXGEN", "vhdxgen", "/tmp/claude-0/vhdxgen-target/release/vhdxgen")
}
func VhdiRead() string { return ToolPath("Z3_VHDIREAD", "vhdiread", "/tmp/claude-0/vhdiread") }

func execCombined(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}
