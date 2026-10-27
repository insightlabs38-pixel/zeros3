package main

import (
	osexec "os/exec"
)

func exec(name string, args ...string) error { return osexec.Command(name, args...).Run() }
