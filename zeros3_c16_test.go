package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func c16Gzip(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	_, err := w.Write(data)
	mustOK(t, err)
	mustOK(t, w.Close())
	return out.Bytes()
}

type c16Case struct {
	src    *authLab
	target manifestV1
	base   xdBaseRef
	gzip   []byte
	thin   []byte
}

func c16Fixture(t *testing.T) c16Case {
	t.Helper()
	src := authMigratedLab(t)
	target := src.man("k3")
	sum, err := decodeHexSHA256(target.ObjectSHA256)
	mustOK(t, err)
	root := mustLoad(t, src.s, sum)
	if len(root.Bases) == 0 {
		t.Fatal("fixture needs a native base")
	}
	base := root.Bases[0]
	for _, data := range src.want {
		if sha256.Sum256(data) == base.SHA {
			var thin bytes.Buffer
			mustOK(t, src.s.ExportAuthorityClosureThin(sum, func(digest [32]byte) bool { return digest == base.SHA }, &thin))
			return c16Case{src: src, target: target, base: base, gzip: c16Gzip(t, data), thin: thin.Bytes()}
		}
	}
	t.Fatal("the exact base is absent from the fixture")
	return c16Case{}
}

func TestC16NamedBaseReplication(t *testing.T) {
	fixture := c16Fixture(t)
	creds := Credentials{AccessKeyID: defaultAccessKeyID, SecretAccessKey: defaultSecretAccessKey}
	src := httptest.NewServer(NewServer(fixture.src.s, creds, defaultRegion))
	defer src.Close()
	for _, key := range []string{"related", "replica"} {
		t.Run(key, func(t *testing.T) {
			dst := newAuthLab(t)
			dst.put(key, fixture.gzip)
			server := httptest.NewServer(NewServer(dst.s, creds, defaultRegion))
			defer server.Close()
			conf := replicateConfig{Source: syncClientConfig{Endpoint: src.URL, Bucket: "b", Key: "k3", Creds: creds, Region: defaultRegion},
				Dest:            syncClientConfig{Endpoint: server.URL, Bucket: "b", Key: "replica", Creds: creds, Region: defaultRegion},
				PreferAuthority: true, AuthorityBaseKey: key, DeriveAuthorityBase: true}
			stats, err := replicateObject(conf)
			if err != nil || stats.AuthorityBytes <= 0 || stats.AuthorityBytes >= fixture.target.TotalLength {
				t.Fatalf("expected a successful thin transfer: %+v %v", stats, err)
			}
			pin := mustLoad(t, dst.s, fixture.base.SHA)
			if pin == nil || pin.Form != authFormPin || pin.Size != fixture.base.Size {
				t.Fatal("verified named base was not installed as a pin")
			}
			first, _, err := dst.s.HeadObject("b", "replica")
			mustOK(t, err)
			_, err = replicateObject(conf)
			mustOK(t, err)
			next, _, err := dst.s.HeadObject("b", "replica")
			mustOK(t, err)
			if first.manifestUUID != next.manifestUUID {
				t.Fatal("a retry changed the verified destination version")
			}
			server.Close()
			dst.want["replica"] = fixture.src.want["k3"]
			dst.reclaim()
			dst.exact("reopened derived-base transfer")
			dst.verifyOK("reopened derived-base transfer")
		})
	}
}

func c16ImportWire(t *testing.T, dst *authLab, fixture c16Case, mutate func(*authImportRequest)) []byte {
	t.Helper()
	entry, man, err := dst.s.HeadObject("b", "related")
	mustOK(t, err)
	control := authImportRequest{
		Version: authXferVersion, SHA256: fixture.target.ObjectSHA256,
		Derived: &authBaseIdentity{Bucket: "b", Key: "related", VersionID: entry.manifestUUID,
			ObjectSHA: man.ObjectSHA256, BaseSHA: hex.EncodeToString(fixture.base.SHA[:]), BaseSize: fixture.base.Size},
		syncCommitRequest: syncCommitRequest{
			Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm,
			Bucket: "b", Key: "replica", ExpectAbsent: true,
		},
	}
	for _, c := range fixture.target.Chunks {
		control.Chunks = append(control.Chunks, syncChunkDescriptor{SHA256: c.SHA256, Length: c.Length})
	}
	if mutate != nil {
		mutate(&control)
	}
	header, err := json.Marshal(control)
	mustOK(t, err)
	return append(append(binary.BigEndian.AppendUint32(nil, uint32(len(header))), header...), fixture.thin...)
}

func TestC16NamedBaseChecksScopeAndFrame(t *testing.T) {
	fx := c16Fixture(t)
	for _, scenario := range []string{"unmatched-scope", "stale-version", "bad-frame", "wrong-size"} {
		t.Run(scenario, func(t *testing.T) {
			dst := newAuthLab(t)
			dst.put("related", fx.gzip)
			wire := c16ImportWire(t, dst, fx, func(req *authImportRequest) {
				switch scenario {
				case "unmatched-scope":
					req.Derived.Bucket = "foreign"
				case "wrong-size":
					req.Derived.BaseSize--
				}
			})
			switch scenario {
			case "stale-version":
				dst.put("related", bytes.Repeat([]byte("new ordinary object"), 128))
			case "bad-frame":
				wire[len(wire)-1] ^= 1
			}
			res := authorityImportRequestForTest(t, dst.s, wire)
			if res.Code == http.StatusOK {
				t.Fatal("invalid named base import succeeded")
			}
			if pin, err := dst.s.authLoad(fx.base.SHA); err != nil || pin != nil {
				t.Fatalf("invalid import published a derived record: %v", err)
			}
			if _, _, err := dst.s.HeadObject("b", "replica"); !errors.Is(err, errNoSuchKey) {
				t.Fatalf("invalid import changed namespace: %v", err)
			}
			dst.exact("after refused named base")
		})
	}
}

func TestC16NamedBaseDiscoveryAuthentication(t *testing.T) {
	dst := newAuthLab(t)
	dst.put("related", c16Gzip(t, []byte("named plaintext")))
	entry, man, err := dst.s.HeadObject("b", "related")
	mustOK(t, err)
	req := authBaseIdentity{Bucket: "b", Key: "related", VersionID: entry.manifestUUID, ObjectSHA: man.ObjectSHA256}
	payload, err := json.Marshal(req)
	mustOK(t, err)
	creds := Credentials{AccessKeyID: defaultAccessKeyID, SecretAccessKey: defaultSecretAccessKey}
	for _, historical := range []bool{true, false} {
		request := httptest.NewRequest(http.MethodPost, zeros3AuthBasePath, bytes.NewReader(payload))
		if historical {
			mustOK(t, signSigV4Request(request, creds, defaultRegion, fmt.Sprintf("%x", sha256.Sum256(payload)), time.Now().Add(-24*time.Hour)))
		}
		res := httptest.NewRecorder()
		NewServer(dst.s, creds, defaultRegion).ServeHTTP(res, request)
		if res.Code == http.StatusOK {
			t.Fatal("unauthenticated or expired request received a base digest")
		}
	}
	if files, err := dst.s.authFiles(); err != nil || len(files) != 0 {
		t.Fatalf("base discovery wrote authority records: %v %v", files, err)
	}
}

func TestC16NamedBaseDecodedBudget(t *testing.T) {
	dst := newAuthLab(t)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	zero := make([]byte, 64<<10)
	for range maxAuthBaseOutput / len(zero) {
		_, err := writer.Write(zero)
		mustOK(t, err)
	}
	_, err := writer.Write([]byte{1})
	mustOK(t, err)
	mustOK(t, writer.Close())
	dst.put("over-budget", compressed.Bytes())
	entry, man, err := dst.s.HeadObject("b", "over-budget")
	mustOK(t, err)
	identity := authBaseIdentity{Bucket: "b", Key: "over-budget", VersionID: entry.manifestUUID, ObjectSHA: man.ObjectSHA256}
	if _, err := dst.s.deriveNamedAuthBase(context.Background(), identity, false); err == nil {
		t.Fatal("over-budget gzip plaintext was accepted")
	}
	if files, err := dst.s.authFiles(); err != nil || len(files) != 0 {
		t.Fatalf("oversized base published a record: %v %v", files, err)
	}
}

func TestC16DerivedBaseImportCuts(t *testing.T) {
	fx := c16Fixture(t)
	for _, cut := range []string{hookAuthImportChunks, hookAfterManifestPublished, hookAfterJournalSync} {
		t.Run(cut, func(t *testing.T) {
			dst := newAuthLab(t)
			dst.put("related", fx.gzip)
			wire := c16ImportWire(t, dst, fx, nil)
			file := filepath.Join(t.TempDir(), "derived.bin")
			mustOK(t, os.WriteFile(file, wire, 0o600))
			dst.close()
			if killed, trace := runHelper(t, "import-request", dst.dir, cut, 1, "ZS3_AUTH_FILE="+file); !killed {
				t.Fatalf("child missed its publication cut: %s", trace)
			}
			dst.open()
			committed := cut == hookAfterJournalSync
			entry, _, err := dst.s.HeadObject("b", "replica")
			if committed && err != nil || !committed && !errors.Is(err, errNoSuchKey) {
				t.Fatalf("wrong conformance after cut: %v", err)
			}
			dst.verifyOK("after derived-base cut")
			if res := authorityImportRequestForTest(t, dst.s, wire); res.Code != http.StatusOK {
				t.Fatalf("retry failed: %d %s", res.Code, res.Body.String())
			}
			after, _, err := dst.s.HeadObject("b", "replica")
			mustOK(t, err)
			if committed && after.manifestUUID != entry.manifestUUID {
				t.Fatal("a crash retry changed the acknowledged version")
			}
			dst.want["replica"] = fx.src.want["k3"]
			dst.exact("derived-base retry")
			dst.verifyOK("derived-base retry")
		})
	}
}
