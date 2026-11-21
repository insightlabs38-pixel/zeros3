//go:build c2recall

package main

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAuthoritySimilaritySampledExactOracle(t *testing.T) {
	store := os.Getenv("ZEROS3_C2_ORACLE_STORE")
	targetKey := os.Getenv("ZEROS3_C2_ORACLE_TARGET")
	baseKeys := strings.Split(os.Getenv("ZEROS3_C2_ORACLE_BASES"), ",")
	output := os.Getenv("ZEROS3_C2_ORACLE_OUT")
	if store == "" || targetKey == "" || len(baseKeys) == 0 || baseKeys[0] == "" || output == "" {
		t.Fatal("set ZEROS3_C2_ORACLE_STORE, ZEROS3_C2_ORACLE_TARGET, ZEROS3_C2_ORACLE_BASES and ZEROS3_C2_ORACLE_OUT")
	}
	lock, err := acquireStoreLock(store, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	s, err := OpenStore(store)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	query, err := s.openCurrentAuthSimilarityQuery()
	if err != nil {
		t.Fatal(err)
	}
	defer query.close()
	const bucket = "fixture"
	readManifest := func(key string) *authObj {
		t.Helper()
		entry, _, err := s.headServed(bucket, key)
		if err != nil {
			t.Fatal(err)
		}
		man, err := s.readVerifiedManifest(entry.manifestUUID, entry.manifestSHA256)
		if err != nil {
			t.Fatal(err)
		}
		sha, err := decodeHexSHA256(man.ObjectSHA256)
		if err != nil {
			t.Fatal(err)
		}
		return &authObj{sha: sha, man: man, bucket: bucket, key: key}
	}
	m := &authMigrator{
		pol:          AuthPolicy{Bucket: bucket, Candidates: 4, MaxDepth: 2, similarity: query},
		indexedBases: make(map[[32]byte]int),
		byKey:        make(map[string][]int),
		depth:        make(map[int]int),
	}
	for i, key := range baseKeys {
		base := readManifest(key)
		rec, err := s.authLoad(base.sha)
		if err != nil || rec == nil || rec.Depth >= 2 {
			t.Fatalf("oracle fixture needs an existing eligible base at %s: %+v %v", key, rec, err)
		}
		m.order = append(m.order, base)
		m.depth[i] = int(rec.Depth)
		m.addBase(i)
	}
	target := readManifest(targetKey)
	m.order = append(m.order, target)
	in := s.newAuthObjAt(target.man)
	defer in.close()
	features, err := authSketchAt(in, target.man.TotalLength, 16)
	if err != nil {
		t.Fatal(err)
	}
	targetIndex := len(m.order) - 1
	start := time.Now()
	ranked := m.candidates(targetIndex, features, true)
	lookupNanos := time.Since(start).Nanoseconds()
	if query.failed || len(ranked) == 0 {
		t.Fatalf("indexed lookup returned no eligible base: failed=%t candidates=%v", query.failed, ranked)
	}

	type choice struct {
		Key          string `json:"key"`
		EncodedBytes int64  `json:"encoded_bytes"`
		Depth        int    `json:"depth"`
	}
	candidates := make([]choice, 0, len(baseKeys))
	for j, src := range m.order[:targetIndex] {
		var row choice
		t.Run(src.key, func(t *testing.T) {
			body := s.newAuthObjAt(src.man)
			defer body.close()
			params := authWParams(1, src.man.TotalLength)
			anchors, err := dwDcoreBuildWIndex(body, src.man.TotalLength, params.Anchor, params.Workers, &src.sha)
			if err != nil {
				t.Fatal(err)
			}
			writer := s.newAuthPieceWriter()
			defer writer.abort()
			if _, err := dwDcoreWEncode(writer, in, target.man.TotalLength, body, src.man.TotalLength, anchors, params); err != nil {
				t.Fatal(err)
			}
			pieces, err := writer.finish()
			if err != nil {
				t.Fatal(err)
			}
			depth := m.depth[j] + 1
			rec := &authRecord{
				SHA: target.sha, Size: target.man.TotalLength, Gen: 1, Form: authFormDelta,
				Pieces: pieces, Depth: byte(depth), Sketch: features,
				Bases: []xdBaseRef{{SHA: src.sha, Size: src.man.TotalLength}},
			}
			if _, _, err := s.authProve(rec, 0); err != nil {
				t.Fatalf("oracle candidate did not reproduce target SHA-256 %x: %v", target.sha, err)
			}
			row = choice{src.key, pieceTotal(pieces), depth}
		})
		if row.EncodedBytes == 0 {
			return
		}
		candidates = append(candidates, row)
	}
	var rankedKeys []string
	for _, j := range ranked {
		rankedKeys = append(rankedKeys, m.order[j].key)
	}
	best := slices.MinFunc(candidates, func(a, b choice) int {
		return cmp.Or(cmp.Compare(a.EncodedBytes, b.EncodedBytes), strings.Compare(a.Key, b.Key))
	})
	result := map[string]any{
		"target": targetKey, "choices": candidates, "index_rank": rankedKeys,
		"oracle": best, "recall_at_1": rankedKeys[0] == best.Key,
		"recall_at_4": slices.Contains(rankedKeys, best.Key), "lookup_ns": lookupNanos,
	}
	f, err := os.CreateTemp(filepath.Dir(output), ".c2-recall-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(result); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(f.Name(), output); err != nil {
		t.Fatal(err)
	}
}
