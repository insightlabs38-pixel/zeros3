package core

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Default credentials of a `zeros3` binary started without credential flags.
const (
	DefaultAccessKey = "AKIAZEROS3EXAMPLE01"
	DefaultSecretKey = "zeros3exampleSecretKeyForM1TestingOnly01"
)

// Server is a managed ZeroS3 process (fixture mode only; the portable
// scenarios never need one).
type Server struct {
	Endpoint string
	Addr     string
	cmd      *exec.Cmd
}

// StartServer launches `bin serve` on a free loopback port over storeDir and
// waits until it accepts connections. Stdout/stderr go to the harness's.
func StartServer(bin, storeDir, region string, extra ...string) (*Server, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := l.Addr().String()
	l.Close()
	args := append([]string{"-store", storeDir, "-addr", addr, "-region", region, "-access-key", DefaultAccessKey, "-secret-key", DefaultSecretKey}, extra...)
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return &Server{Endpoint: "http://" + addr, Addr: addr, cmd: cmd}, nil
		}
	}
	_ = cmd.Process.Kill()
	return nil, fmt.Errorf("zeros3 did not listen on %s", addr)
}

// Stop shuts the server down gracefully (SIGTERM) and waits for it.
func (s *Server) Stop() {
	if s.cmd == nil {
		return
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = s.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
	}
	s.cmd = nil
}
