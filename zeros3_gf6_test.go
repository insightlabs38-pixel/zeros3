package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type gf6Link struct {
	delay                    time.Duration
	rate                     int64
	sent, received, requests atomic.Int64
	cut                      atomic.Bool
}

type gf6Body struct {
	io.ReadCloser
	bytes              *atomic.Int64
	rate, limit, total int64
	began              time.Time
}

func (b *gf6Body) Read(p []byte) (int, error) {
	if b.limit > 0 {
		if b.total >= b.limit {
			return 0, io.ErrUnexpectedEOF
		}
		p = p[:min(int64(len(p)), b.limit-b.total)]
	}
	n, err := b.ReadCloser.Read(p)
	b.total += int64(n)
	b.bytes.Add(int64(n))
	if b.rate > 0 {
		deadline := b.began.Add(time.Duration(b.total) * time.Second / time.Duration(b.rate))
		if remaining := time.Until(deadline); remaining > 0 {
			time.Sleep(remaining)
		}
	}
	return n, err
}

func (l *gf6Link) RoundTrip(original *http.Request) (*http.Response, error) {
	l.requests.Add(1)
	if l.delay > 0 {
		select {
		case <-time.After(l.delay):
		case <-original.Context().Done():
			return nil, original.Context().Err()
		}
	}
	req := original.Clone(original.Context())
	if req.Body != nil {
		req.Body = &gf6Body{ReadCloser: req.Body, bytes: &l.sent, rate: l.rate, began: time.Now()}
		req.GetBody = nil
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	limit := int64(0)
	if req.URL.Path == zeros3AuthExportPath && resp.StatusCode == http.StatusOK && l.cut.CompareAndSwap(true, false) {
		limit = 129
	}
	resp.Body = &gf6Body{ReadCloser: resp.Body, bytes: &l.received, rate: l.rate, limit: limit, began: time.Now()}
	return resp, nil
}

var gf6BaseKeys = []string{"authorized/old", "authorized/near"}

func gf6Data(edits int) ([][]byte, []byte) {
	old := genRandomBytes(87291, 256<<10)
	near := slices.Clone(old)
	copy(near[55<<10:190<<10], genRandomBytes(811562, 135<<10))
	target := slices.Clone(near)
	for i := range edits {
		target[(31913+i*19997)%len(target)] ^= byte(1 + i%169)
	}
	return [][]byte{old, near}, target
}

func gf6Pin(t *testing.T, l *authLab, key string, raw []byte) {
	t.Helper()
	o := &authObj{sha: sha256.Sum256(raw), man: l.man(key), key: key, bucket: "b"}
	mustOK(t, l.s.authEnsurePin(o))
}

// Describe only explicitly named bases in the selected destination scope.
// The authenticated result is an optimization hint. The native closure
// importer still verifies all bytes and rechecks the namespace condition.
func gf6Probe(t *testing.T, dst syncClientConfig, bases [][]byte) []int {
	t.Helper()
	items := make([]nativeBatchObject, len(gf6BaseKeys))
	for i, key := range gf6BaseKeys {
		items[i] = nativeBatchObject{Bucket: dst.Bucket, Key: key}
	}
	reply, err := nativeBatchItems(dst, nativeBatchDescribe, items)
	mustOK(t, err)
	var matched []int
	for i, r := range reply {
		if r.Status == http.StatusNotFound {
			continue
		}
		desc, err := nativeObjectDescription(r, items[i])
		mustOK(t, err)
		if desc.Authoritative && desc.Size == int64(len(bases[i])) && desc.ObjectSHA256 == fmt.Sprintf("%x", sha256.Sum256(bases[i])) {
			matched = append(matched, i)
		}
	}
	return matched
}

func gf6Compile(t *testing.T, l *authLab, choices []int, bases [][]byte, target []byte) ([]int, int) {
	t.Helper()
	best := len(target)
	var picked []int
	var blob []byte
	var refs []xdBaseRef
	try := func(indices []int) {
		source := make([]xdSrc, 0, len(indices))
		for _, i := range indices {
			if bytes.Equal(bases[i], target) {
				return
			}
			source = append(source, xdSrc{sha256.Sum256(bases[i]), bases[i]})
		}
		coded, used, err := xdEncode(source, target, 32<<10)
		mustOK(t, err)
		if len(used) == 0 || len(coded)+512 >= best {
			return
		}
		picked = nil
		for _, x := range used {
			for i, v := range source {
				if v.sha == x.SHA {
					picked = append(picked, indices[i])
					break
				}
			}
		}
		best, blob, refs = len(coded), coded, used
	}
	for i, k := range choices {
		try([]int{k})
		for _, j := range choices[i+1:] {
			try([]int{k, j})
		}
	}
	if len(picked) == 0 {
		return nil, 0
	}
	for _, k := range picked {
		gf6Pin(t, l, gf6BaseKeys[k], bases[k])
	}
	pieces, err := l.s.authWritePieces(t.Context(), blob)
	mustOK(t, err)
	rec := &authRecord{SHA: sha256.Sum256(target), Size: int64(len(target)), Gen: 1, Form: authFormDelta, Depth: 1, Bases: refs, Pieces: pieces}
	_, _, err = l.s.authProve(rec, 6)
	mustOK(t, err)
	mustOK(t, l.s.authPublish(rec))
	return picked, best
}

func gf6Plain(t *testing.T, src, dst syncClientConfig) {
	t.Helper()
	response, err := src.signAndDoStream(t.Context(), http.MethodGet, "/b/"+src.Key, nil, nil)
	mustOK(t, err)
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	mustOK(t, err)
	if response.StatusCode != http.StatusOK {
		t.Fatal("plain source read failed")
	}
	response, err = dst.signAndDoStream(t.Context(), http.MethodPut, "/b/"+dst.Key, data, nil)
	mustOK(t, err)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("plain destination write failed")
	}
	_, err = io.Copy(io.Discard, response.Body)
	mustOK(t, err)
}

func gf6Trial(t *testing.T, policy, topology string, edits int, delay time.Duration, rate int64, cut bool) {
	t.Helper()
	a, b := newAuthLab(t), newAuthLab(t)
	basis, target := gf6Data(edits)
	if topology == "both" {
		// The current exact destination revision independently names two
		// complementary bases. The next target draws from both as well.
		copy(basis[1][:52<<10], genRandomBytes(72512, 52<<10))
		copy(target[:52<<10], basis[0][:52<<10])
		copy(target[190<<10:], basis[1][190<<10:])
	}
	for i, key := range gf6BaseKeys {
		a.put(key, basis[i])
	}
	const srcKey = "publish/value"
	const dstKey = "receive/value"
	a.put(srcKey, target)
	switch topology {
	case "near":
		b.put(gf6BaseKeys[1], basis[1])
		gf6Pin(t, b, gf6BaseKeys[1], basis[1])
	case "both":
		for i, key := range gf6BaseKeys {
			b.put(key, basis[i])
			gf6Pin(t, b, key, basis[i])
		}
		prior := slices.Clone(target)
		prior[41023] ^= 73
		b.put(dstKey, prior)
		if used, _ := gf6Compile(t, b, []int{0, 1}, basis, prior); len(used) != 2 {
			t.Fatal("expected prepared complementary destination revision")
		}
	case "ordinary":
		b.put(gf6BaseKeys[1], basis[1])
	case "unrelated":
		other := genRandomBytes(507, 256<<10)
		b.put(gf6BaseKeys[1], other)
		gf6Pin(t, b, gf6BaseKeys[1], other)
	case "empty":
	default:
		t.Fatal("unknown receiver fixture")
	}
	if edits == 0 && topology == "near" {
		b.put(dstKey, target)
	}
	_, src := fb16TestServer(t, a, "gf6-source", true, false)
	_, dst := fb16TestServer(t, b, "gf6-destination", true, false)
	src.Key, dst.Key = srcKey, dstKey
	from, to := &gf6Link{delay: delay, rate: rate}, &gf6Link{delay: delay, rate: rate}
	src.HTTPClient, dst.HTTPClient = &http.Client{Transport: from, Timeout: 30 * time.Second}, &http.Client{Transport: to, Timeout: 30 * time.Second}
	start := time.Now()
	var base []int
	encoded := 0
	prepCPU, prepWall, probeWall := int64(0), int64(0), int64(0)
	if policy == "prepared" || policy == "joint" {
		allowed := []int{0}
		if policy == "joint" {
			now := time.Now()
			allowed = gf6Probe(t, dst, basis)
			probeWall = time.Since(now).Nanoseconds()
		}
		c, now := fb13CPU(), time.Now()
		base, encoded = gf6Compile(t, a, allowed, basis, target)
		prepCPU, prepWall = int64(fb13CPU()-c), time.Since(now).Nanoseconds()
	}
	if policy == "prepared" {
		start = time.Now()
	} // Amortized source preparation, also reported.
	cpu := fb13CPU()
	var authBytes int64
	switch policy {
	case "plain":
		gf6Plain(t, src, dst)
	case "v3":
		r, err := replicateNamespace(namespaceReplicateConfig{Source: src, Dest: dst, SourcePrefix: "publish", DestPrefix: "receive", NativeBatch: true, Incremental: true, Workers: 2})
		mustOK(t, err)
		if !r.OK() {
			t.Fatalf("native v3 failed: %+v", r)
		}
	default:
		cfg := replicateConfig{Source: src, Dest: dst, PreferAuthority: len(base) > 0, IfUnchanged: true, Workers: 2}
		if len(base) == 1 {
			cfg.AuthorityBaseKey = gf6BaseKeys[base[0]]
		} else if len(base) > 1 {
			cfg.AuthorityBaseKey = dst.Key
		}
		if cut {
			from.cut.Store(true)
			if _, err := replicateObject(cfg); err == nil || from.cut.Load() {
				t.Fatal("did not intercept and reject a truncated closure")
			}
			if _, _, err := b.s.HeadObject("b", dstKey); !errors.Is(err, errNoSuchKey) {
				t.Fatal("partial closure changed destination")
			}
			b.close()
			b.open()
			// Install a new loopback service after the destination store reopened.
			_, fresh := fb16TestServer(t, b, "gf6-resume", true, false)
			fresh.Key, fresh.HTTPClient = dstKey, dst.HTTPClient
			cfg.Dest = fresh
		}
		result, err := replicateObject(cfg)
		mustOK(t, err)
		authBytes = result.AuthorityBytes
	}
	timeNS, transferCPU := time.Since(start).Nanoseconds(), int64(fb13CPU()-cpu)
	got := b.read(dstKey, byteRange{0, int64(len(target)) - 1})
	if !bytes.Equal(got, target) || sha256.Sum256(got) != sha256.Sum256(target) {
		t.Fatal("replicated bytes differ")
	}
	b.verifyOK("joint receiver proof")
	if topology == "both" && (len(base) != 2 || authBytes == 0) {
		t.Fatal("complementary closure was not transported")
	}
	if topology == "unrelated" && policy == "joint" && len(base) > 0 {
		t.Fatal("an unrelated authorized basis was selected")
	}
	row, err := json.Marshal(map[string]any{"policy": policy, "topology": topology, "edits": edits, "delay_ms": delay.Milliseconds(), "rate_Bps": rate,
		"base": base, "encoded_bytes": encoded, "authority_bytes": authBytes, "cut": cut, "wall_ns": timeNS,
		"source_prepare_cpu_ns": prepCPU, "source_prepare_wall_ns": prepWall, "probe_wall_ns": probeWall, "transfer_process_cpu_ns": transferCPU,
		"source_requests": from.requests.Load(), "target_requests": to.requests.Load(), "source_request_bytes": from.sent.Load(),
		"source_response_bytes": from.received.Load(), "target_request_bytes": to.sent.Load(), "target_response_bytes": to.received.Load()})
	mustOK(t, err)
	fmt.Printf("GF6 %s\n", row)
}

func TestGF6SignedNegotiation(t *testing.T) {
	for _, edits := range []int{0, 1, 7, 1000} {
		for _, policy := range []string{"plain", "v3", "prepared", "joint"} {
			t.Run(fmt.Sprintf("%d/%s", edits, policy), func(t *testing.T) { gf6Trial(t, policy, "near", edits, 0, 0, false) })
		}
	}
	for _, topology := range []string{"empty", "ordinary", "unrelated"} {
		t.Run(topology, func(t *testing.T) { gf6Trial(t, "joint", topology, 7, 0, 0, false) })
	}
	t.Run("complementary", func(t *testing.T) { gf6Trial(t, "joint", "both", 7, 0, 0, false) })
	t.Run("cut-and-reopen", func(t *testing.T) { gf6Trial(t, "joint", "near", 7, 0, 0, true) })
}

func TestGF6ShapedSignedNegotiation(t *testing.T) {
	for _, delay := range []time.Duration{0, 20 * time.Millisecond, 80 * time.Millisecond, 150 * time.Millisecond} {
		for _, policy := range []string{"plain", "v3", "joint"} {
			pair := strings.ReplaceAll(delay.String(), ".", "-") + "/" + policy
			t.Run(pair, func(t *testing.T) { gf6Trial(t, policy, "near", 7, delay, 1<<20, false) })
		}
	}
}
