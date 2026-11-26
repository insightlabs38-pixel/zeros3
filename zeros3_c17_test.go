package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"testing"
)

func TestC17RetainedCanonicalPlan(t *testing.T) {
	l := newAuthLab(t)
	original := b8Tree(1703, 14)
	plain := b8Tar(t, original, 1700001500)
	l.put("source.tar", plain)
	l.put("early.tar.gz", b8Gzip(t, plain, 6))
	l.put("later.tar.gz", b8Gzip(t, b8Tar(t, b8Edit(original, 1407), 1700001600), 9))
	l.reclaim()
	rep := b8Optimize(t, l, OptimizePolicy{RetainCanonicalPlans: true})
	if rep.Canon == nil || rep.Canon.RetainedHits == 0 || rep.Canon.RetainedBytes <= 0 ||
		rep.Canon.RetainedBytes > canonPlanBytes || rep.Canon.RetainedEntries > canonPlanEntries ||
		rep.Failed != 0 {
		t.Fatalf("canonical plan reuse did not complete: %+v", rep)
	}
	l.reclaim()
	l.exact("reopened retained canonical plans")
	l.verifyOK("reopened retained canonical plans")
}

func TestC17CanonicalBudgetAndDigest(t *testing.T) {
	l := newAuthLab(t)
	g := &rgGraph{s: l.s, canonSt: CanonStats{RetainedBytes: canonPlanBytes - 12}}
	data := []byte("retained component")
	sum := sha256.Sum256(data)
	rep := &authRepCand{chunks: []chunkRef{{SHA256: hex.EncodeToString(sum[:]), Length: int64(len(data))}}}
	mem := map[[32]byte][]byte{sum: data}
	g.retainCanonPlan(rep, mem)
	if rep.retained != nil {
		t.Fatal("over-budget recipe was retained")
	}
	g.canonSt.RetainedBytes = 0
	g.retainCanonPlan(rep, mem)
	if len(rep.retained) != 1 {
		t.Fatal("small verified recipe was not retained")
	}
	data[0] ^= 1
	if err := g.publishRetainedCanon(context.Background(), &rgNode{size: int64(len(data))}, rep); !errors.Is(err, errAuthMismatch) {
		t.Fatalf("modified retained component was staged: %v", err)
	}
}

func TestC17FusedIngestKeepsFailedPacksPrivate(t *testing.T) {
	defer func(old int64) { directPackTarget = old }(directPackTarget)
	directPackTarget = 512 << 10
	old := ingestAblate
	defer func() { ingestAblate = old }()
	ingestAblate.pipeMinMiB = 1
	content := genRandomBytes(171991, 1700<<10)
	sha := sha256.Sum256(content)
	for _, cores := range []int{1, 2} {
		t.Run(fmt.Sprint(cores), func(t *testing.T) {
			prior := runtime.GOMAXPROCS(cores)
			defer runtime.GOMAXPROCS(prior)
			l := newAuthLab(t)
			before := len(tierPackIDs(l.dir, tierHot))
			for _, how := range []string{"digest", "truncated", "canceled"} {
				ref := xdBaseRef{SHA: sha, Size: int64(len(content))}
				data := content
				ctx, cancel := context.WithCancel(context.Background())
				switch how {
				case "digest":
					ref.SHA[0] ^= 17
				case "truncated":
					data = content[:len(content)-11]
				case "canceled":
					cancel()
				}
				err := l.s.installDerivedAuthPin(ctx, ref, &derivedBaseReader{ctx, bytes.NewReader(data)})
				cancel()
				if err == nil || len(tierPackIDs(l.dir, tierHot)) != before {
					t.Fatalf("%s published a staging pack or accepted invalid input: %v", how, err)
				}
				if pin, err := l.s.authLoad(ref.SHA); err != nil || pin != nil {
					t.Fatalf("%s exposed an authority pin: %v", how, err)
				}
			}
			ref := xdBaseRef{SHA: sha, Size: int64(len(content))}
			mustOK(t, l.s.installDerivedAuthPin(context.Background(), ref, bytes.NewReader(content)))
			if len(tierPackIDs(l.dir, tierHot)) != before+1 {
				t.Fatal("verified staging did not publish exactly one complete pack")
			}
			pin := mustLoad(t, l.s, sha)
			if pin.Form != authFormPin || pin.Size != ref.Size {
				t.Fatal("valid verified ingest did not create its pin")
			}
			_, _, err := l.s.authProve(pin, 0)
			mustOK(t, err)
		})
	}
}
