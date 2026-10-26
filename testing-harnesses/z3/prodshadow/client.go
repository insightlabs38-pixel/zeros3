package prodshadow

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrNotFound reports a CAS object that is in neither loose nor packed form.
var ErrNotFound = errors.New("prodshadow: not in the CAS")

// ErrCorrupt reports a chunk whose every stored copy failed production verification.
var ErrCorrupt = errors.New("prodshadow: chunk is corrupt")

// Loc is one packed copy of a chunk as the production locator reports it.
type Loc struct {
	Pack    string `json:"pack"`
	Tier    string `json:"tier"`
	Codec   int    `json:"codec"` // 0 raw, 1 DEFLATE
	Stored  uint32 `json:"stored"`
	Logical uint32 `json:"logical"`
}

// Where is the physical placement of one chunk.
type Where struct {
	SHA      string `json:"sha"`
	Size     int64  `json:"size"`
	Loose    bool   `json:"loose"`
	LooseLen int64  `json:"loose_len"`
	Packed   []Loc  `json:"packed"`
}

// Put is one chunk written through the production CAS batch; Existed means the
// CAS already held it (stored by the authoritative CDC path or an earlier put).
type Put struct {
	SHA     string `json:"sha"`
	Existed bool   `json:"existed"`
}

// Bridge is a running instance of the real store behind the lab API.
type Bridge struct {
	cmd    *exec.Cmd
	base   string
	hc     *http.Client
	OpenMS float64 // OpenStore (locator load) time
	Packs  int
	done   chan struct{}
}

// Start opens the store directory with the bridge binary. env adds variables
// (for example Z3_SHADOW_LIVE).
func (b *Binaries) Start(storeDir string, env ...string) (*Bridge, error) {
	cmd := exec.Command(b.Shadow)
	cmd.Env = append(os.Environ(), append([]string{"Z3_BRIDGE_STORE=" + storeDir}, env...)...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, fmt.Errorf("prodshadow: bridge did not start: %v: %s", err, stderr.String())
	}
	var hello struct {
		Addr   string  `json:"addr"`
		OpenMS float64 `json:"open_ms"`
		Packs  int     `json:"packs"`
	}
	if err := json.Unmarshal([]byte(line), &hello); err != nil {
		cmd.Process.Kill()
		return nil, err
	}
	br := &Bridge{cmd: cmd, base: "http://" + hello.Addr, OpenMS: hello.OpenMS, Packs: hello.Packs, done: make(chan struct{}),
		hc: &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64}, Timeout: 5 * time.Minute}}
	go func() { io.Copy(io.Discard, out); cmd.Wait(); close(br.done) }()
	return br, nil
}

// Stop shuts the store down cleanly (releases the lock) and waits for exit.
func (br *Bridge) Stop() error {
	resp, err := br.hc.Post(br.base+"/shutdown", "text/plain", nil)
	if err == nil {
		resp.Body.Close()
	}
	select {
	case <-br.done:
		return nil
	case <-time.After(20 * time.Second):
		br.Kill()
		return errors.New("prodshadow: bridge did not stop")
	}
}

// Kill is an abrupt process death (SIGKILL): no Close, no flush.
func (br *Bridge) Kill() {
	br.cmd.Process.Kill()
	<-br.done
}

func (br *Bridge) get(path string) ([]byte, int, error) {
	resp, err := br.hc.Get(br.base + path)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

// Stat returns a chunk's physical placement.
func (br *Bridge) Stat(sha [32]byte) (Where, error) {
	b, code, err := br.get("/cas/stat?sha=" + hex.EncodeToString(sha[:]))
	if err != nil {
		return Where{}, err
	}
	if code == 404 {
		return Where{}, ErrNotFound
	}
	if code != 200 {
		return Where{}, fmt.Errorf("prodshadow: stat: %d %s", code, b)
	}
	var w Where
	return w, json.Unmarshal(b, &w)
}

// Read is the production casRead: the whole chunk, verified against its digest
// (copies that fail verification are skipped by the store).
func (br *Bridge) Read(sha [32]byte) ([]byte, error) {
	b, code, err := br.get("/cas/read?sha=" + hex.EncodeToString(sha[:]))
	if err != nil {
		return nil, err
	}
	switch code {
	case 200:
		return b, nil
	case 404:
		return nil, ErrNotFound
	case 422:
		return nil, fmt.Errorf("%w: %x", ErrCorrupt, sha[:6])
	}
	return nil, fmt.Errorf("prodshadow: read %x: %d %s", sha[:6], code, strings.TrimSpace(string(b)))
}

// PutBatch publishes chunks through the production CAS batch (staged, fsynced,
// then renamed under the publication barrier).
func (br *Bridge) PutBatch(chunks [][]byte) ([]Put, error) {
	var body bytes.Buffer
	for _, c := range chunks {
		var h [4]byte
		binary.BigEndian.PutUint32(h[:], uint32(len(c)))
		body.Write(h[:])
		body.Write(c)
	}
	resp, err := br.hc.Post(br.base+"/cas/putbatch", "application/octet-stream", &body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("prodshadow: putbatch: %d %s", resp.StatusCode, b)
	}
	var out []Put
	return out, json.Unmarshal(b, &out)
}

func (br *Bridge) s3(method, key string, body []byte, hdr map[string]string) ([]byte, int, error) {
	req, _ := http.NewRequest(method, br.base+"/s3/shadow/"+key, bytes.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := br.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

// S3Put is an ordinary authoritative PutObject (CDC) through the real handler.
func (br *Bridge) S3Put(key string, data []byte) error {
	b, code, err := br.s3("PUT", key, data, nil)
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("prodshadow: PUT %s: %d %s", key, code, b)
	}
	return nil
}

// S3Get is an ordinary GET.
func (br *Bridge) S3Get(key string) ([]byte, error) {
	b, code, err := br.s3("GET", key, nil, nil)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("prodshadow: GET %s: %d %s", key, code, b)
	}
	return b, nil
}

// S3Range is an ordinary single-range GET.
func (br *Bridge) S3Range(key string, off, n int64) ([]byte, error) {
	b, code, err := br.s3("GET", key, nil, map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", off, off+n-1)})
	if err != nil {
		return nil, err
	}
	if code != 206 {
		return nil, fmt.Errorf("prodshadow: range GET %s: %d %s", key, code, b)
	}
	return b, nil
}

// S3Delete deletes the current object (history keeps its manifest).
func (br *Bridge) S3Delete(key string) error {
	b, code, err := br.s3("DELETE", key, nil, nil)
	if err != nil {
		return err
	}
	if code/100 != 2 {
		return fmt.Errorf("prodshadow: DELETE %s: %d %s", key, code, b)
	}
	return nil
}

// Touch lists the production chunks (and their placement) an authoritative
// range read of key needs.
func (br *Bridge) Touch(key string, off, n int64) ([]Where, error) {
	b, code, err := br.get(fmt.Sprintf("/obj/touch?bucket=shadow&key=%s&off=%d&len=%d", key, off, n))
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("prodshadow: touch: %d %s", code, b)
	}
	var w []Where
	return w, json.Unmarshal(b, &w)
}

// PeakRSSKiB is the bridge process's peak resident set (VmHWM), 0 if unavailable.
func (br *Bridge) PeakRSSKiB() int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", br.cmd.Process.Pid))
	if err != nil {
		return 0
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "VmHWM:") {
			var n int64
			fmt.Sscanf(strings.TrimPrefix(ln, "VmHWM:"), "%d", &n)
			return n
		}
	}
	return 0
}
