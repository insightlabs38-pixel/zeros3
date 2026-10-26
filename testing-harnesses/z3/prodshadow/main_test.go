package prodshadow

import (
	"fmt"
	"os"
	"testing"
)

var bins *Binaries

func TestMain(m *testing.M) {
	var err error
	if bins, err = Build(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
