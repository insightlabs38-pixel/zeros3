package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	accessKey = "AKIAZEROS3EXAMPLE01"
	secretKey = "zeros3exampleSecretKeyForM1TestingOnly01"
	region    = "us-east-1"
)

func hm(k []byte, s string) []byte {
	h := hmac.New(sha256.New, k)
	h.Write([]byte(s))
	return h.Sum(nil)
}

// sign adds SigV4 header auth. payload is the x-amz-content-sha256 value
// (hex digest, UNSIGNED-PAYLOAD or a STREAMING-* sentinel).
func sign(req *http.Request, payload string, signedExtra ...string) {
	signFull(req, payload, signedExtra...)
}

type sigInfo struct {
	seed  string
	key   []byte
	scope string
	amz   string
}

func signFull(req *http.Request, payload string, signedExtra ...string) sigInfo {
	now := time.Now().UTC()
	amz := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("x-amz-date", amz)
	req.Header.Set("x-amz-content-sha256", payload)
	hdrs := map[string]string{"host": req.URL.Host, "x-amz-content-sha256": payload, "x-amz-date": amz}
	for _, h := range signedExtra {
		hdrs[strings.ToLower(h)] = strings.TrimSpace(req.Header.Get(h))
	}
	names := make([]string, 0, len(hdrs))
	for k := range hdrs {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, k := range names {
		ch.WriteString(k + ":" + hdrs[k] + "\n")
	}
	signed := strings.Join(names, ";")
	q := req.URL.Query()
	qk := make([]string, 0, len(q))
	for k := range q {
		qk = append(qk, k)
	}
	sort.Strings(qk)
	var qs []string
	for _, k := range qk {
		for _, v := range q[k] {
			qs = append(qs, strings.ReplaceAll(url.QueryEscape(k), "+", "%20")+"="+strings.ReplaceAll(url.QueryEscape(v), "+", "%20"))
		}
	}
	canon := strings.Join([]string{req.Method, req.URL.EscapedPath(), strings.Join(qs, "&"), ch.String(), signed, payload}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(canon))
	sts := "AWS4-HMAC-SHA256\n" + amz + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hm(hm(hm(hm([]byte("AWS4"+secretKey), day), region), "s3"), "aws4_request")
	sig := hex.EncodeToString(hm(k, sts))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
	return sigInfo{seed: sig, key: k, scope: scope, amz: amz}
}
