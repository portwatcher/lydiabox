package s3proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const (
	testAccess = "lydiabox"
	testSecret = "admin-token-0123456789"
	testRegion = "us-east-1"
)

var testTime = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func creds(secret string) aws.Credentials {
	return aws.Credentials{AccessKeyID: testAccess, SecretAccessKey: secret}
}

func s3Opts(o *v4.SignerOptions) { o.DisableURIPathEscaping = true }

// signHeader signs req like the S3 SDK does: x-amz-content-sha256 set
// first, then SigV4 with path escaping disabled.
func signHeader(t *testing.T, req *http.Request, payloadHash, secret string, at time.Time) {
	t.Helper()
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if err := v4.NewSigner().SignHTTP(context.Background(), creds(secret), req, payloadHash, "s3", testRegion, at, s3Opts); err != nil {
		t.Fatal(err)
	}
}

// presign returns a presigned URL for method and rawURL valid for expires.
func presign(t *testing.T, method, rawURL string, expires time.Duration, secret string, at time.Time) string {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := req.URL.Query()
	q.Set("X-Amz-Expires", strconv.Itoa(int(expires/time.Second)))
	req.URL.RawQuery = q.Encode()
	u, _, err := v4.NewSigner().PresignHTTP(context.Background(), creds(secret), req, unsignedPayload, "s3", testRegion, at, s3Opts)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// serverSide converts a client request into the request a server handler
// sees (RequestURI, decoded URL, Host).
func serverSide(t *testing.T, req *http.Request) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if err := req.Write(&buf); err != nil {
		t.Fatal(err)
	}
	r, err := http.ReadRequest(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func newTestProxy(t *testing.T, backend string, now time.Time) *Proxy {
	t.Helper()
	u, err := url.Parse(backend)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Options{AccessKey: testAccess, SecretKey: testSecret, Backend: u, Prefix: "/s3", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// backendCheck verifies a request the way an S3 server does, with the SDK
// signer: header auth is signed again over the received request (only the
// headers the client signed), presigned URLs are presigned again. It
// returns the signature the backend expects and the one it received.
func backendCheck(r *http.Request) (want, got string, err error) {
	if r.URL.Query().Has(qSignature) {
		q := r.URL.Query()
		got = q.Get(qSignature)
		at, err := time.Parse(amzDateFormat, q.Get(qDate))
		if err != nil {
			return "", "", err
		}
		q.Del(qSignature)
		u := &url.URL{Scheme: "http", Host: r.Host, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: q.Encode()}
		clone, _ := http.NewRequest(r.Method, u.String(), nil)
		clone.Host = r.Host
		signed, _, err := v4.NewSigner().PresignHTTP(context.Background(), creds(testSecret), clone, unsignedPayload, "s3", testRegion, at, s3Opts)
		if err != nil {
			return "", "", err
		}
		su, _ := url.Parse(signed)
		return su.Query().Get(qSignature), got, nil
	}
	a, serr := parseAuthorization(r.Header.Get("Authorization"))
	if serr != nil {
		return "", "", serr
	}
	at, err := time.Parse(amzDateFormat, r.Header.Get("X-Amz-Date"))
	if err != nil {
		return "", "", err
	}
	u := &url.URL{Scheme: "http", Host: r.Host, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}
	clone, _ := http.NewRequest(r.Method, u.String(), nil)
	clone.Host = r.Host
	clone.ContentLength = r.ContentLength
	for _, n := range strings.Split(a.signedHeaders, ";") {
		if n != "host" && n != "content-length" {
			clone.Header[http.CanonicalHeaderKey(n)] = r.Header.Values(n)
		}
	}
	hash := r.Header.Get("X-Amz-Content-Sha256")
	if err := v4.NewSigner().SignHTTP(context.Background(), creds(testSecret), clone, hash, "s3", testRegion, at, s3Opts); err != nil {
		return "", "", err
	}
	w, _ := parseAuthorization(clone.Header.Get("Authorization"))
	if w.signedHeaders != a.signedHeaders {
		return "", "", fmt.Errorf("signed headers %q, backend signed %q", a.signedHeaders, w.signedHeaders)
	}
	return w.signature, a.signature, nil
}

// errorCode decodes an S3 XML error response.
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/xml" {
		t.Fatalf("Content-Type %q, body %s", ct, rec.Body)
	}
	var e xmlError
	if err := xml.NewDecoder(io.Reader(rec.Body)).Decode(&e); err != nil {
		t.Fatal(err)
	}
	return e.Code
}
