package s3proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SigV4 constants.
const (
	algorithm        = "AWS4-HMAC-SHA256"
	amzDateFormat    = "20060102T150405Z"
	scopeTerminator  = "aws4_request"
	serviceS3        = "s3"
	emptySHA256      = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	unsignedPayload  = "UNSIGNED-PAYLOAD"
	streamingSigned  = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	streamingTrailer = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
	maxPresignExpiry = 7 * 24 * time.Hour
)

// Presigned URL query parameters.
const (
	qAlgorithm     = "X-Amz-Algorithm"
	qCredential    = "X-Amz-Credential"
	qDate          = "X-Amz-Date"
	qExpires       = "X-Amz-Expires"
	qSignedHeaders = "X-Amz-SignedHeaders"
	qSignature     = "X-Amz-Signature"
)

// scope is the credential scope of a signature:
// <access>/<date>/<region>/<service>/aws4_request.
type scope struct {
	accessKey, date, region, service string
}

// String returns the scope without the access key, as in the string to sign.
func (s scope) String() string {
	return s.date + "/" + s.region + "/" + s.service + "/" + scopeTerminator
}

func parseCredential(v string) (scope, *s3Error) {
	parts := strings.Split(v, "/")
	if len(parts) != 5 || parts[0] == "" || len(parts[1]) != 8 || parts[2] == "" {
		return scope{}, errInvalidRequest("malformed credential %q", v)
	}
	if parts[3] != serviceS3 || parts[4] != scopeTerminator {
		return scope{}, errInvalidRequest("the credential scope must end in /s3/aws4_request")
	}
	return scope{accessKey: parts[0], date: parts[1], region: parts[2], service: parts[3]}, nil
}

// authorization is a parsed `Authorization: AWS4-HMAC-SHA256 ...` header.
type authorization struct {
	credential    string
	signedHeaders string
	signature     string
}

func parseAuthorization(h string) (authorization, *s3Error) {
	rest, ok := strings.CutPrefix(h, algorithm+" ")
	if !ok {
		return authorization{}, errInvalidRequest("unsupported authorization type; use AWS Signature Version 4 (%s)", algorithm)
	}
	var a authorization
	for _, field := range strings.Split(rest, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok {
			return authorization{}, errInvalidRequest("malformed Authorization header")
		}
		switch k {
		case "Credential":
			a.credential = v
		case "SignedHeaders":
			a.signedHeaders = v
		case "Signature":
			a.signature = v
		}
	}
	if a.credential == "" || a.signedHeaders == "" || a.signature == "" {
		return authorization{}, errInvalidRequest("the Authorization header needs Credential, SignedHeaders, and Signature")
	}
	return a, nil
}

// signedHeaderList validates a SignedHeaders value: lower-case names,
// separated by ';', with "host" included.
func signedHeaderList(v string) ([]string, *s3Error) {
	names := strings.Split(v, ";")
	host := false
	for _, n := range names {
		if n == "" || n != strings.ToLower(n) {
			return nil, errInvalidRequest("malformed SignedHeaders %q", v)
		}
		if n == "host" {
			host = true
		}
	}
	if !host {
		return nil, errInvalidRequest("SignedHeaders must include host")
	}
	return names, nil
}

// canonicalHeaders returns the canonical header block for names, read from
// the incoming request.
func canonicalHeaders(r *http.Request, names []string) string {
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(':')
		switch n {
		case "host":
			b.WriteString(strings.TrimSpace(stripExcessSpaces(r.Host)))
		case "content-length":
			v := r.Header.Get("Content-Length")
			if v == "" && r.ContentLength >= 0 {
				v = strconv.FormatInt(r.ContentLength, 10)
			}
			b.WriteString(strings.TrimSpace(v))
		default:
			for i, v := range r.Header.Values(n) {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(strings.TrimSpace(stripExcessSpaces(v)))
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// stripExcessSpaces collapses runs of spaces to one space.
func stripExcessSpaces(s string) string {
	if !strings.Contains(s, "  ") {
		return s
	}
	var b strings.Builder
	space := false
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			if space {
				continue
			}
			space = true
		} else {
			space = false
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// canonicalQuery encodes the query parameters sorted by key, then value.
// skipSignature drops X-Amz-Signature (presigned URLs).
func canonicalQuery(rawQuery string, skipSignature bool) (string, error) {
	type param struct{ k, v string }
	var params []param
	for _, part := range strings.Split(rawQuery, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		dk, err := url.QueryUnescape(k)
		if err != nil {
			return "", err
		}
		dv, err := url.QueryUnescape(v)
		if err != nil {
			return "", err
		}
		if skipSignature && dk == qSignature {
			continue
		}
		params = append(params, param{uriEncode(dk, true), uriEncode(dv, true)})
	}
	sort.Slice(params, func(i, j int) bool {
		if params[i].k != params[j].k {
			return params[i].k < params[j].k
		}
		return params[i].v < params[j].v
	})
	var b strings.Builder
	for i, p := range params {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
	}
	return b.String(), nil
}

// uriEncode percent-encodes every byte except the unreserved characters
// A-Z a-z 0-9 - _ . ~ (and '/' unless encodeSlash), with upper-case hex.
func uriEncode(s string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' || (c == '/' && !encodeSlash) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}

// request is everything a SigV4 signature covers, except the secret.
type request struct {
	method        string
	uri           string
	query         string // canonical
	headers       string // canonical block
	signedHeaders string
	payloadHash   string
	amzDate       string
	scope         scope
}

func (q request) canonical() string {
	return strings.Join([]string{q.method, q.uri, q.query, q.headers, q.signedHeaders, q.payloadHash}, "\n")
}

// signature returns the hex SigV4 signature of q, made with key (see
// signingKey).
func (q request) signature(key []byte) string {
	return hex.EncodeToString(hmacSHA256(key, []byte(q.stringToSign())))
}

func (q request) stringToSign() string {
	return algorithm + "\n" + q.amzDate + "\n" + q.scope.String() + "\n" + sha256Hex([]byte(q.canonical()))
}

func signingKey(secret string, s scope) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), []byte(s.date))
	k = hmacSHA256(k, []byte(s.region))
	k = hmacSHA256(k, []byte(s.service))
	return hmacSHA256(k, []byte(scopeTerminator))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// signatureEqual compares two hex signatures in constant time.
func signatureEqual(a, b string) bool {
	return hmac.Equal([]byte(strings.ToLower(a)), []byte(strings.ToLower(b)))
}
