package webrtc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v4"
	pion "github.com/pion/webrtc/v4"
)

// tcpTURN runs a TURN server that clients reach over TCP (or TLS when cert
// is set) on 127.0.0.1 and that relays UDP on 127.0.0.1.
func tcpTURN(t *testing.T, cert *tls.Certificate) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	must(t, err)
	if cert != nil {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS12})
	}
	lf := logging.NewDefaultLoggerFactory()
	lf.DefaultLogLevel = logging.LogLevelDisabled
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: turnRealm, AuthHandler: turn.LongTermTURNRESTAuthHandler(turnSecret, nil), LoggerFactory: lf,
		ListenerConfigs: []turn.ListenerConfig{{Listener: ln,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP("127.0.0.1"), Address: "127.0.0.1"}}},
	})
	must(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func relayOnlyRig(t *testing.T, url string) {
	t.Helper()
	srv := turnServer(t, url)
	r := newRig(t, rigConfig{loopback: true,
		guest:   PeerConfig{ICEServers: []pion.ICEServer{srv}, RelayOnly: true},
		browser: pion.Configuration{ICEServers: []pion.ICEServer{srv}, ICETransportPolicy: pion.ICETransportPolicyRelay}})
	r.expectMedia(t)
	if g, b := r.pairs(t); g != "relay/relay" || b != "relay/relay" {
		t.Fatalf("selected pairs: guest %s, browser %s", g, b)
	}
}

// TURN over TCP, for networks that block UDP.
func TestRelayOnlyTURNOverTCP(t *testing.T) {
	port := tcpTURN(t, nil)
	relayOnlyRig(t, "turn:127.0.0.1:"+strconv.Itoa(port)+"?transport=tcp")
}

// TURN over TLS (turns:). pion verifies the server certificate against the
// system roots, which only Linux lets a test replace (SSL_CERT_FILE); the
// guest runs on Linux.
func TestRelayOnlyTURNOverTLS(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs SSL_CERT_FILE support of the Linux system roots")
	}
	cert, pemBytes := selfSigned(t, "localhost")
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	must(t, os.WriteFile(caFile, pemBytes, 0o600))
	t.Setenv("SSL_CERT_FILE", caFile)
	t.Setenv("SSL_CERT_DIR", t.TempDir())
	port := tcpTURN(t, &cert)
	relayOnlyRig(t, "turns:localhost:"+strconv.Itoa(port)+"?transport=tcp")
}

func selfSigned(t *testing.T, host string) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	must(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
