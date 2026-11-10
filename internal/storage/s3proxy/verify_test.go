package s3proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestVerifyHeaderAuth checks signatures made by the aws-sdk-go-v2 signer.
func TestVerifyHeaderAuth(t *testing.T) {
	p := newTestProxy(t, "http://backend", testTime)
	body := "hello, s3"
	sum := sha256.Sum256([]byte(body))
	cases := []struct {
		name, method, url, body, hash string
	}{
		{"list buckets", "GET", "http://box:8080/s3/", "", emptySHA256},
		{"list objects", "GET", "http://box:8080/s3/lydiabox?list-type=2&prefix=workspace%2F&delimiter=%2F", "", emptySHA256},
		{"put escaped key", "PUT", "http://box:8080/s3/lydiabox/workspace/a%20b%2Bc%21.txt?x-id=PutObject", body, hex.EncodeToString(sum[:])},
		{"unsigned payload", "PUT", "http://box:8080/s3/lydiabox/workspace/u.txt", body, unsignedPayload},
		{"multipart upload", "POST", "http://box:8080/s3/lydiabox/big.bin?uploads", "", emptySHA256},
		{"unicode key", "GET", "http://box:8080/s3/lydiabox/workspace/%E6%96%87%E4%BB%B6.md", "", emptySHA256},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest(c.method, c.url, strings.NewReader(c.body))
			req.Header.Set("Content-Type", "text/plain")
			signHeader(t, req, c.hash, testSecret, testTime)
			r := serverSide(t, req)
			v, serr := p.verify(r)
			if serr != nil {
				t.Fatalf("verify: %v", serr)
			}
			if v.presigned || v.req.payloadHash != c.hash {
				t.Fatalf("verified %+v", v)
			}

			bad, _ := http.NewRequest(c.method, c.url, strings.NewReader(c.body))
			signHeader(t, bad, c.hash, "wrong-secret", testTime)
			if _, serr := p.verify(serverSide(t, bad)); serr == nil || serr.code != "SignatureDoesNotMatch" || serr.status != 403 {
				t.Fatalf("wrong secret: %v", serr)
			}

			tampered := r.Clone(r.Context())
			tampered.Header.Set("Content-Type", "application/octet-stream")
			if _, serr := p.verify(tampered); serr == nil || serr.code != "SignatureDoesNotMatch" {
				t.Fatalf("tampered header: %v", serr)
			}
		})
	}
}

func TestVerifyRejections(t *testing.T) {
	p := newTestProxy(t, "http://backend", testTime)
	signed := func(at time.Time, mutate func(*http.Request)) *http.Request {
		req, _ := http.NewRequest("GET", "http://box:8080/s3/lydiabox/k", nil)
		signHeader(t, req, emptySHA256, testSecret, at)
		r := serverSide(t, req)
		if mutate != nil {
			mutate(r)
		}
		return r
	}
	cases := []struct {
		name   string
		req    *http.Request
		status int
		code   string
	}{
		{"anonymous", httptest.NewRequest("GET", "/s3/lydiabox/k", nil), 403, "AccessDenied"},
		{"skew past", signed(testTime.Add(-16*time.Minute), nil), 403, "RequestTimeTooSkewed"},
		{"skew future", signed(testTime.Add(16*time.Minute), nil), 403, "RequestTimeTooSkewed"},
		{"within skew", signed(testTime.Add(-14*time.Minute), nil), 200, ""},
		{"sigv2", signed(testTime, func(r *http.Request) { r.Header.Set("Authorization", "AWS lydiabox:abc=") }), 400, "InvalidRequest"},
		{"no signature", signed(testTime, func(r *http.Request) {
			r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=lydiabox/20261110/us-east-1/s3/aws4_request, SignedHeaders=host")
		}), 400, "InvalidRequest"},
		{"other access key", signed(testTime, func(r *http.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "Credential=lydiabox/", "Credential=admin/", 1))
		}), 403, "InvalidAccessKeyId"},
		{"other service", signed(testTime, func(r *http.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "/s3/aws4_request", "/iam/aws4_request", 1))
		}), 400, "InvalidRequest"},
		{"date mismatch", signed(testTime, func(r *http.Request) { r.Header.Set("X-Amz-Date", "20261109T120000Z") }), 400, "InvalidRequest"},
		{"header and presign", signed(testTime, func(r *http.Request) { r.URL.RawQuery = "X-Amz-Signature=00" }), 400, "InvalidRequest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, serr := p.verify(c.req)
			if c.status == 200 {
				if serr != nil {
					t.Fatalf("want success, got %v", serr)
				}
				return
			}
			if serr == nil || serr.status != c.status || serr.code != c.code {
				t.Fatalf("got %v, want %d %s", serr, c.status, c.code)
			}
		})
	}
}

func TestVerifyPresigned(t *testing.T) {
	p := newTestProxy(t, "http://backend", testTime)
	u := presign(t, "GET", "http://box:8080/s3/lydiabox/workspace/a%20b.txt", 10*time.Minute, testSecret, testTime.Add(-time.Minute))
	req, _ := http.NewRequest("GET", u, nil)
	v, serr := p.verify(serverSide(t, req))
	if serr != nil || !v.presigned || v.req.payloadHash != unsignedPayload {
		t.Fatalf("presigned: %v %+v", serr, v)
	}

	cases := []struct {
		name string
		url  string
		now  time.Time
		code string
	}{
		{"expired", u, testTime.Add(10 * time.Minute), "AccessDenied"},
		{"not yet valid", presign(t, "GET", "http://box:8080/s3/lydiabox/k", time.Hour, testSecret, testTime.Add(20*time.Minute)), testTime, "AccessDenied"},
		{"wrong secret", presign(t, "GET", "http://box:8080/s3/lydiabox/k", time.Hour, "wrong", testTime), testTime, "SignatureDoesNotMatch"},
		{"other key", strings.Replace(u, "a%20b.txt", "c.txt", 1), testTime, "SignatureDoesNotMatch"},
		{"expiry too long", presign(t, "GET", "http://box:8080/s3/lydiabox/k", 8*24*time.Hour, testSecret, testTime), testTime, "InvalidRequest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pp := newTestProxy(t, "http://backend", c.now)
			req, _ := http.NewRequest("GET", c.url, nil)
			if _, serr := pp.verify(serverSide(t, req)); serr == nil || serr.code != c.code {
				t.Fatalf("got %v, want %s", serr, c.code)
			}
		})
	}
}

func TestErrorDocument(t *testing.T) {
	p := newTestProxy(t, "http://backend", testTime)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/s3/lydiabox/k", nil))
	if rec.Code != 403 || errorCode(t, rec) != "AccessDenied" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/s3/lydiabox/k", nil)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 garbage")
	p.ServeHTTP(rec, r)
	if rec.Code != 400 || errorCode(t, rec) != "InvalidRequest" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestCanonicalQuery(t *testing.T) {
	got, err := canonicalQuery("prefix=a%20b&list-type=2&uploads&delimiter=%2F&b=2&b=1&X-Amz-Signature=ff", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := "b=1&b=2&delimiter=%2F&list-type=2&prefix=a%20b&uploads="; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStripAndReplace(t *testing.T) {
	p := newTestProxy(t, "http://backend", testTime)
	for in, want := range map[string]string{"/s3": "/", "/s3/": "/", "/s3/lydiabox/k": "/lydiabox/k"} {
		if got, ok := p.strip(in); !ok || got != want {
			t.Errorf("strip(%q) = %q %v", in, got, ok)
		}
	}
	if _, ok := p.strip("/s3x/a"); ok {
		t.Error("strip accepted /s3x/a")
	}
	got := replaceSignature("a=1&X-Amz-Signature=00&b=%2F", "ff")
	if got != "a=1&X-Amz-Signature=ff&b=%2F" {
		t.Fatal(got)
	}
}
