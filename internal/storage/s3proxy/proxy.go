// Package s3proxy serves an S3 backend under a path prefix of another HTTP
// server. S3 clients sign the request path (AWS Signature Version 4), so a
// proxy that removes a prefix must re-sign. The proxy verifies the client's
// signature with the gateway credentials, removes the prefix, and signs the
// request again for the backend with the same credential scope (same date,
// region, and service). It accepts the Authorization header and presigned
// URLs, and it rewrites the chunk signature chain of
// STREAMING-AWS4-HMAC-SHA256-PAYLOAD[-TRAILER] uploads on the fly.
//
// The front credentials and the backend credentials are the same, so a
// client never needs other credentials than the gateway's.
package s3proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultMaxSkew is the largest accepted difference between X-Amz-Date and
// the server clock.
const DefaultMaxSkew = 15 * time.Minute

// Options configures a Proxy.
type Options struct {
	// AccessKey and SecretKey are the gateway credentials. Clients sign with
	// them, and the proxy signs backend requests with them.
	AccessKey string
	SecretKey string
	// Backend is the S3 server (scheme and host).
	Backend *url.URL
	// Prefix is the public path prefix, for example "/s3".
	Prefix string
	// MaxSkew defaults to DefaultMaxSkew.
	MaxSkew time.Duration
	// Now defaults to time.Now.
	Now func() time.Time
	// Transport defaults to http.DefaultTransport.
	Transport http.RoundTripper
}

// Proxy is the verify-and-re-sign S3 reverse proxy. It is an http.Handler.
type Proxy struct {
	opts Options
	rp   *httputil.ReverseProxy
}

// New validates opts and builds a Proxy.
func New(opts Options) (*Proxy, error) {
	if opts.AccessKey == "" || opts.SecretKey == "" {
		return nil, errors.New("s3proxy: AccessKey and SecretKey are required")
	}
	if opts.Backend == nil || opts.Backend.Host == "" {
		return nil, errors.New("s3proxy: Backend is required")
	}
	if opts.Prefix != "" && (!strings.HasPrefix(opts.Prefix, "/") || strings.HasSuffix(opts.Prefix, "/")) {
		return nil, errors.New("s3proxy: Prefix must start with / and must not end with /")
	}
	if opts.MaxSkew <= 0 {
		opts.MaxSkew = DefaultMaxSkew
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	p := &Proxy{opts: opts}
	p.rp = &httputil.ReverseProxy{
		Rewrite:       p.rewrite,
		Transport:     opts.Transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var se *s3Error
			if errors.As(err, &se) {
				se.write(w, r)
				return
			}
			(&s3Error{http.StatusBadGateway, "InternalError", "the S3 backend is unavailable"}).write(w, r)
		},
	}
	return p, nil
}

type planKey struct{}

// plan is the verified request and its backend form.
type plan struct {
	path, rawPath, rawQuery string
	authorization           string // empty for presigned URLs
}

// ServeHTTP implements http.Handler.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := p.strip(r.URL.Path)
	if !ok {
		errInvalidRequest("the path is outside %s", p.opts.Prefix).write(w, r)
		return
	}
	v, serr := p.verify(r)
	if serr != nil {
		serr.write(w, r)
		return
	}
	pl, chain := p.resign(r, v, rest)
	if chain != nil {
		r.Body = newChunkRewriter(r.Body, *chain)
	}
	p.rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), planKey{}, pl)))
}

// strip removes the prefix: "/s3" and "/s3/" give "/", "/s3/b/k" gives "/b/k".
func (p *Proxy) strip(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, p.opts.Prefix)
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
		return "", false
	}
	if rest == "" {
		rest = "/"
	}
	return rest, true
}

func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	pl, _ := pr.In.Context().Value(planKey{}).(*plan)
	pr.Out.URL.Scheme = p.opts.Backend.Scheme
	pr.Out.URL.Host = p.opts.Backend.Host
	pr.Out.Host = pr.In.Host
	if pl == nil {
		return
	}
	pr.Out.URL.Path = pl.path
	pr.Out.URL.RawPath = pl.rawPath
	pr.Out.URL.RawQuery = pl.rawQuery
	if pl.authorization != "" {
		pr.Out.Header.Set("Authorization", pl.authorization)
	}
}

// verified is a request whose client signature matched.
type verified struct {
	req       request
	key       []byte
	signature string
	auth      authorization // header auth only
	presigned bool
	time      time.Time
}

func (p *Proxy) verify(r *http.Request) (*verified, *s3Error) {
	authz := r.Header.Get("Authorization")
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, errInvalidRequest("malformed query string")
	}
	presigned := query.Has(qAlgorithm) || query.Has(qSignature) || query.Has(qCredential)
	var v *verified
	var serr *s3Error
	switch {
	case authz != "" && presigned:
		return nil, errInvalidRequest("use either the Authorization header or a presigned URL, not both")
	case authz != "":
		v, serr = p.headerAuth(r, authz)
	case presigned:
		v, serr = p.presignAuth(r, query)
	default:
		return nil, errAccessDenied("anonymous access is not allowed; sign the request with AWS Signature Version 4")
	}
	if serr != nil {
		return nil, serr
	}
	// Clients sign the path as sent; some send characters unescaped that
	// others escape, so also try the canonical escaping of the decoded path.
	for _, uri := range []string{r.URL.EscapedPath(), uriEncode(r.URL.Path, false)} {
		v.req.uri = uri
		if signatureEqual(v.req.signature(v.key), v.signature) {
			return v, nil
		}
	}
	return nil, errSignature()
}

var hexSignature = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

func (p *Proxy) headerAuth(r *http.Request, authz string) (*verified, *s3Error) {
	a, serr := parseAuthorization(authz)
	if serr != nil {
		return nil, serr
	}
	sc, serr := p.scope(a.credential)
	if serr != nil {
		return nil, serr
	}
	names, serr := signedHeaderList(a.signedHeaders)
	if serr != nil {
		return nil, serr
	}
	if !hexSignature.MatchString(a.signature) {
		return nil, errInvalidRequest("malformed signature")
	}
	amzDate := r.Header.Get("X-Amz-Date")
	var t time.Time
	if amzDate != "" {
		var err error
		if t, err = time.Parse(amzDateFormat, amzDate); err != nil {
			return nil, errInvalidRequest("malformed X-Amz-Date %q", amzDate)
		}
	} else {
		var err error
		if t, err = http.ParseTime(r.Header.Get("Date")); err != nil {
			return nil, errAccessDenied("the request needs an X-Amz-Date or Date header")
		}
		amzDate = t.UTC().Format(amzDateFormat)
	}
	if t.UTC().Format("20060102") != sc.date {
		return nil, errInvalidRequest("the credential date %s does not match the request date %s", sc.date, amzDate)
	}
	if d := p.opts.Now().Sub(t); d > p.opts.MaxSkew || d < -p.opts.MaxSkew {
		return nil, errSkewed()
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if payload == "" {
		payload = emptySHA256
	}
	if !hexSignature.MatchString(payload) && !strings.HasPrefix(payload, "STREAMING-") && payload != unsignedPayload {
		return nil, errInvalidRequest("unsupported x-amz-content-sha256 %q", payload)
	}
	if strings.HasPrefix(payload, "STREAMING-") && payload != streamingSigned && payload != streamingTrailer &&
		payload != "STREAMING-UNSIGNED-PAYLOAD-TRAILER" {
		return nil, errInvalidRequest("unsupported x-amz-content-sha256 %q", payload)
	}
	q, err := canonicalQuery(r.URL.RawQuery, false)
	if err != nil {
		return nil, errInvalidRequest("malformed query string")
	}
	return &verified{
		req: request{method: r.Method, query: q, headers: canonicalHeaders(r, names), signedHeaders: a.signedHeaders,
			payloadHash: payload, amzDate: amzDate, scope: sc},
		key: signingKey(p.opts.SecretKey, sc), signature: a.signature, auth: a, time: t,
	}, nil
}

func (p *Proxy) presignAuth(r *http.Request, query url.Values) (*verified, *s3Error) {
	if query.Get(qAlgorithm) != algorithm {
		return nil, errInvalidRequest("%s must be %s", qAlgorithm, algorithm)
	}
	sc, serr := p.scope(query.Get(qCredential))
	if serr != nil {
		return nil, serr
	}
	signed := query.Get(qSignedHeaders)
	names, serr := signedHeaderList(signed)
	if serr != nil {
		return nil, serr
	}
	sig := query.Get(qSignature)
	if !hexSignature.MatchString(sig) {
		return nil, errInvalidRequest("malformed or missing %s", qSignature)
	}
	amzDate := query.Get(qDate)
	t, err := time.Parse(amzDateFormat, amzDate)
	if err != nil {
		return nil, errInvalidRequest("malformed or missing %s", qDate)
	}
	if t.Format("20060102") != sc.date {
		return nil, errInvalidRequest("the credential date %s does not match %s %s", sc.date, qDate, amzDate)
	}
	secs, err := strconv.Atoi(query.Get(qExpires))
	expires := time.Duration(secs) * time.Second
	if err != nil || secs < 1 || expires > maxPresignExpiry {
		return nil, errInvalidRequest("%s must be between 1 and 604800 seconds", qExpires)
	}
	now := p.opts.Now()
	if t.Sub(now) > p.opts.MaxSkew {
		return nil, errAccessDenied("Request is not valid yet")
	}
	if now.After(t.Add(expires)) {
		return nil, errAccessDenied("Request has expired")
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if payload == "" {
		payload = unsignedPayload
	}
	q, err := canonicalQuery(r.URL.RawQuery, true)
	if err != nil {
		return nil, errInvalidRequest("malformed query string")
	}
	return &verified{
		req: request{method: r.Method, query: q, headers: canonicalHeaders(r, names), signedHeaders: signed,
			payloadHash: payload, amzDate: amzDate, scope: sc},
		key: signingKey(p.opts.SecretKey, sc), signature: sig, presigned: true, time: t,
	}, nil
}

func (p *Proxy) scope(credential string) (scope, *s3Error) {
	sc, serr := parseCredential(credential)
	if serr != nil {
		return scope{}, serr
	}
	if sc.accessKey != p.opts.AccessKey {
		return scope{}, errInvalidAccessKey()
	}
	return sc, nil
}

// resign signs the backend request: the same request with the prefix
// removed from the path. It returns the chunk chain to rewrite for signed
// streaming uploads.
func (p *Proxy) resign(r *http.Request, v *verified, rest string) (*plan, *chain) {
	back := v.req
	back.uri = uriEncode(rest, false)
	sig := back.signature(v.key)
	pl := &plan{path: rest, rawPath: back.uri, rawQuery: r.URL.RawQuery}
	if v.presigned {
		pl.rawQuery = replaceSignature(r.URL.RawQuery, sig)
		return pl, nil
	}
	pl.authorization = algorithm + " Credential=" + v.auth.credential + ", SignedHeaders=" + v.auth.signedHeaders +
		", Signature=" + sig
	if v.req.payloadHash != streamingSigned && v.req.payloadHash != streamingTrailer {
		return pl, nil
	}
	return pl, &chain{
		key: v.key, amzDate: v.req.amzDate, scope: v.req.scope.String(),
		clientSeed: strings.ToLower(v.signature), backendSeed: sig, trailer: v.req.payloadHash == streamingTrailer,
	}
}

// replaceSignature replaces the X-Amz-Signature value and keeps every other
// query byte unchanged.
func replaceSignature(rawQuery, sig string) string {
	parts := strings.Split(rawQuery, "&")
	for i, part := range parts {
		k, _, _ := strings.Cut(part, "=")
		if dk, err := url.QueryUnescape(k); err == nil && dk == qSignature {
			parts[i] = qSignature + "=" + sig
		}
	}
	return strings.Join(parts, "&")
}
