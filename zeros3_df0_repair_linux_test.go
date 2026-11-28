package main

import (
	"bytes"
	"crypto/sha256"
	"io"
	"testing"
)

func TestDF0GenericRepairDiscovery(t *testing.T) {
	raw := gf2TarFixture(t, false)
	base := gf2GzipFixture(t, raw, 1, 65<<10, "source.tar")
	target := gf2GzipFixture(t, raw, 9, 83<<10, "target.tar")
	l := newAuthLab(t)
	l.put("base", base)
	l.put("target", target)
	mustOK(t, l.s.ensureFeature(agFeature))
	gf7Publish(t, l, base, target, l.man("base"), l.man("target"))
	mustOK(t, l.s.DeleteObject("b", "base"))
	delete(l.want, "base")
	_, err := l.s.finishAuthorityArchive(t.Context(), 0)
	mustOK(t, err)
	l.verifyOK("DF0 exact authority before missing component")
	digest := sha256.Sum256(target)
	var wire bytes.Buffer
	mustOK(t, l.s.ExportAuthorityClosure(digest, &wire))
	peer := newAuthLab(t)
	mustOK(t, peer.s.ImportAuthorityClosure(&wire))
	peer.put("target", target)
	_, cfg := fb16TestServer(t, peer, "df0-repair", true, false)
	cfg.Key = "target"
	rec, err := l.s.authLoad(digest)
	mustOK(t, err)
	body, err := l.s.casRead(rec.Root.Rep)
	mustOK(t, err)
	rep, err := unmarshalRep(body, repProdLimits)
	mustOK(t, err)
	p, err := parseDeflateTranslate(rep.Recipe)
	mustOK(t, err)
	victim := rep.Members[len(p.index.Sources)].SHA
	removeChunkAnyLayout(t, l.s, victim)
	l.close()
	l.open()
	outcome, err := l.s.Verify(true)
	mustOK(t, err)
	if outcome.OK() {
		t.Fatal("missing frame passed exact native verification")
	}
	stats, repairErr := l.s.repairFromPeer(repairConfig{Peer: cfg, Out: io.Discard, Workers: 1})
	_, missing := l.s.casRead(victim)
	if missing != nil {
		mustOK(t, l.s.recoverAuthorityFromPeer("b", "target", "", cfg))
	}
	l.close()
	l.open()
	l.exact("DF0 scoped or generic signed repair exact")
	l.verifyOK("DF0 repaired translation")
	df0Log(t, "DF0REPAIR", map[string]any{
		"generic_repair_error": errorText(repairErr), "generic_found": stats.BadChunks,
		"generic_restored_exact_frame": missing == nil, "final_scoped_or_generic_verified": true,
	})
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
