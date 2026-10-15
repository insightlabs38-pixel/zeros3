package ociz3

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Client is a minimal anonymous pull client for the OCI distribution API: bearer
// token negotiation (WWW-Authenticate), manifests and blobs by tag or digest,
// retry on 429/5xx, a hard byte budget, and a digest-keyed cache outside Git. Every
// blob and every digest-addressed manifest is verified (size and sha256) before it
// is cached or returned. It is research tooling; a production client would do more.
type Client struct {
	Host     string // e.g. "quay.io"
	HTTP     *http.Client
	CacheDir string // blobs are cached as <dir>/<hex>
	Budget   int64  // remaining bytes this client may download (<=0 = unlimited)
	Fetched  int64  // bytes downloaded (not served from cache)
	Requests int
	tokens   map[string]string
}

func NewClient(host, cacheDir string, budget int64) *Client {
	return &Client{Host: host, HTTP: &http.Client{Timeout: 10 * time.Minute}, CacheDir: cacheDir, Budget: budget, tokens: map[string]string{}}
}

const acceptManifests = "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json"

func (c *Client) do(repo, path, accept string) (*http.Response, error) {
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequest("GET", "https://"+c.Host+"/v2/"+repo+path, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if t := c.tokens[repo]; t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
		c.Requests++
		resp, err := c.HTTP.Do(req)
		if err != nil {
			last = err
			time.Sleep(time.Duration(1<<attempt) * time.Second)
			continue
		}
		switch {
		case resp.StatusCode == 401 && c.tokens[repo] == "":
			ch := resp.Header.Get("Www-Authenticate")
			resp.Body.Close()
			tok, err := c.token(ch, repo)
			if err != nil {
				return nil, err
			}
			c.tokens[repo] = tok
			continue
		case resp.StatusCode == 429 || resp.StatusCode >= 500:
			wait := time.Duration(1<<attempt) * 2 * time.Second
			if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 && ra < 120 {
				wait = time.Duration(ra) * time.Second
			}
			resp.Body.Close()
			last = fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
			time.Sleep(wait)
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("registry %s: %w", c.Host, last)
}

// token follows a Bearer challenge (realm, service) with scope repository:<repo>:pull.
func (c *Client) token(challenge, repo string) (string, error) {
	rest, ok := strings.CutPrefix(challenge, "Bearer ")
	if !ok {
		return "", fmt.Errorf("registry %s: unsupported auth challenge %q", c.Host, challenge)
	}
	params := map[string]string{}
	for _, kv := range strings.Split(rest, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if ok {
			params[k] = strings.Trim(v, `"`)
		}
	}
	realm := params["realm"]
	u, err := url.Parse(realm)
	if err != nil || u.Scheme != "https" {
		return "", fmt.Errorf("registry %s: bad token realm %q", c.Host, realm)
	}
	q := u.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	q.Set("scope", "repository:"+repo+":pull")
	u.RawQuery = q.Encode()
	resp, err := c.HTTP.Get(u.String())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("registry %s: token HTTP %d", c.Host, resp.StatusCode)
	}
	var tr struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", err
	}
	if tr.Token != "" {
		return tr.Token, nil
	}
	return tr.AccessToken, nil
}

// Manifest fetches a manifest or index by tag or digest. When ref is a digest the
// body is verified against it; the returned digest is always the sha256 of the
// returned bytes (what a digest-pinned fetch would name).
func (c *Client) Manifest(repo, ref string) (digest string, body []byte, err error) {
	if hexs, derr := ParseDigest(ref); derr == nil {
		if b := c.cached(hexs); b != nil {
			return ref, b, nil
		}
	}
	resp, err := c.do(repo, "/manifests/"+ref, acceptManifests)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", nil, fmt.Errorf("%s/%s manifests/%s: HTTP %d", c.Host, repo, ref, resp.StatusCode)
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, int64(DefaultLimits().MaxJSON)+1))
	if err != nil {
		return "", nil, err
	}
	if len(body) > DefaultLimits().MaxJSON {
		return "", nil, fmt.Errorf("%w: manifest larger than %d", ErrLimit, DefaultLimits().MaxJSON)
	}
	digest = Sum256(body)
	if _, derr := ParseDigest(ref); derr == nil && digest != ref {
		return "", nil, fmt.Errorf("%w: manifest %s hashes to %s", ErrDigest, ref, digest)
	}
	c.store(strings.TrimPrefix(digest, "sha256:"), body)
	return digest, body, nil
}

func (c *Client) cached(hexs string) []byte {
	if c.CacheDir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(c.CacheDir, hexs))
	if err != nil {
		return nil
	}
	s := sha256.Sum256(b)
	if hex.EncodeToString(s[:]) != hexs {
		os.Remove(filepath.Join(c.CacheDir, hexs))
		return nil
	}
	return b
}

func (c *Client) store(hexs string, b []byte) {
	if c.CacheDir == "" {
		return
	}
	if os.MkdirAll(c.CacheDir, 0o755) == nil {
		tmp := filepath.Join(c.CacheDir, hexs+".part")
		if os.WriteFile(tmp, b, 0o644) == nil {
			os.Rename(tmp, filepath.Join(c.CacheDir, hexs))
		}
	}
}

// Blob fetches a blob by digest, checking the descriptor size first against the
// budget and verifying size and sha256 of the bytes received.
func (c *Client) Blob(repo, digest string, size int64) ([]byte, error) {
	hexs, err := ParseDigest(digest)
	if err != nil {
		return nil, err
	}
	if size < 0 || size > DefaultLimits().MaxBlob {
		return nil, fmt.Errorf("%w: blob %s declares %d bytes", ErrLimit, short(digest), size)
	}
	if b := c.cached(hexs); b != nil && int64(len(b)) == size {
		return b, nil
	}
	if c.Budget > 0 && c.Fetched+size > c.Budget {
		return nil, fmt.Errorf("%w: blob %s (%d bytes) would exceed the %d byte budget (%d fetched)", ErrLimit, short(digest), size, c.Budget, c.Fetched)
	}
	resp, err := c.do(repo, "/blobs/"+digest, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s/%s blobs/%s: HTTP %d", c.Host, repo, short(digest), resp.StatusCode)
	}
	var buf bytes.Buffer
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(&buf, h), io.LimitReader(resp.Body, size+1))
	if err != nil {
		return nil, err
	}
	c.Fetched += n
	if n != size || hex.EncodeToString(h.Sum(nil)) != hexs {
		return nil, fmt.Errorf("%w: blob %s: got %d bytes, sha256 %x", ErrDigest, short(digest), n, h.Sum(nil))
	}
	c.store(hexs, buf.Bytes())
	return buf.Bytes(), nil
}

// FetchImage fetches the index (or manifest) at indexDigest and, for every
// manifest that matches one of platforms ("os/arch[/variant]"; empty = all
// non-attestation manifests), its config and layers. It returns the exact root
// document and a store holding every verified blob. Attestation manifests and
// other non-image manifests are skipped (listed in notes).
func (c *Client) FetchImage(repo, rootDigest string, platforms []string) (root []byte, st MemStore, notes []string, err error) {
	_, root, err = c.Manifest(repo, rootDigest)
	if err != nil {
		return nil, nil, nil, err
	}
	st = MemStore{}
	st[rootDigest] = root
	want := map[string]bool{}
	for _, p := range platforms {
		want[p] = true
	}
	ix, ierr := ParseIndex(root, Limits{})
	if ierr != nil {
		return nil, nil, nil, ierr
	}
	// the root is an index; each selected entry is fetched and walked
	for _, d := range ix.Manifests {
		if d.Platform == nil || d.Platform.OS == "unknown" || d.Platform.Architecture == "unknown" {
			notes = append(notes, "skipped non-image manifest "+short(d.Digest))
			continue
		}
		if len(want) > 0 && !want[d.Platform.String()] && !want[d.Platform.OS+"/"+d.Platform.Architecture] {
			continue
		}
		_, mb, err := c.Manifest(repo, d.Digest)
		if err != nil {
			return nil, nil, nil, err
		}
		st[d.Digest] = mb
		m, err := ParseManifest(mb, Limits{})
		if err != nil {
			return nil, nil, nil, err
		}
		for _, bd := range append([]Descriptor{m.Config}, m.Layers...) {
			b, err := c.Blob(repo, bd.Digest, bd.Size)
			if err != nil {
				return nil, nil, nil, err
			}
			st[bd.Digest] = b
		}
	}
	return root, st, notes, nil
}
