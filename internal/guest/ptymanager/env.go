//go:build unix

package ptymanager

import (
	"os"
	"strings"
)

// inheritedNames are the variables of the daemon's environment that every
// PTY gets: the user, the shell, the search path, the locale, the time
// zone, the desktop and session bus, and the network proxy and CA
// settings. inheritedPrefixes adds the locale categories (LC_*).
var inheritedNames = map[string]bool{
	"HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "PATH": true,
	"LANG": true, "LANGUAGE": true, "TZ": true,
	"DISPLAY": true, "XDG_RUNTIME_DIR": true, "DBUS_SESSION_BUS_ADDRESS": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true, "all_proxy": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
}

var inheritedPrefixes = []string{"LC_"}

// Inherited reports whether a variable of the daemon's environment goes
// to the PTYs. Everything else stays out: the variables of the service
// manager and supervisord, the daemon's own LYDIABOX_* settings (the link
// listener, the file roots), and what a supplier's service gives to the
// daemon (for example harness settings, MCP settings, or credentials).
// The PTYs then get a defined environment: these variables, Options.Env
// (the gateway URLs, the placeholder key, the terminal type), and the
// spec's variables (docs/instabox.md, "Environment of LydiaBox
// processes").
func Inherited(name string) bool {
	if inheritedNames[name] {
		return true
	}
	for _, p := range inheritedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// baseEnv is the part of environ (KEY=VALUE entries) that Inherited
// allows, with LANG defaulting to C.UTF-8.
func baseEnv(environ []string) map[string]string {
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && Inherited(k) {
			env[k] = v
		}
	}
	if env["LANG"] == "" {
		env["LANG"] = "C.UTF-8"
	}
	return env
}

// daemonEnviron is os.Environ; tests replace it.
var daemonEnviron = os.Environ
