package instabox

import (
	"errors"
	"net"
	"net/http"
)

// loopbackHost reports whether host is localhost or a loopback address.
func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// keepKeyOnRedirect returns a copy of hc that does not send the API key
// to another host on a redirect (net/http removes only Authorization and
// Cookie), and refuses a redirect from https to http.
func keepKeyOnRedirect(hc *http.Client) *http.Client {
	c := *hc
	next := hc.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		first := via[0].URL
		if first.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("instabox: redirect from https to http refused")
		}
		if req.URL.Host != first.Host {
			req.Header.Del("X-API-Key")
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return errors.New("instabox: stopped after 10 redirects")
		}
		return nil
	}
	return &c
}
