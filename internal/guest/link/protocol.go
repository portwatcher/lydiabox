// Package link carries the guest daemon transport over one WebSocket that
// the host dials, for a guest that the host cannot reach with vsock or a
// shared UNIX socket (a hosted machine behind an HTTPS proxy).
//
// One link carries both directions. After a mutual handshake that proves
// knowledge of the link secret, both sides run a yamux session on the
// WebSocket. Each yamux stream starts with a 4-byte big-endian port number:
// the host opens streams to guest port 9000 (the RPC), and the guest opens
// streams to host ports 9001 (connector broker) and 9002 (LLM gateway).
// yamux gives each stream its own flow-control window, so a slow stream
// does not block the others.
//
// This package holds the protocol and the guest side; internal/vmm/link
// holds the host side.
package link

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// Subprotocol is the WebSocket subprotocol of the link.
const Subprotocol = "lydiabox-link.v1"

// Environment variables of the guest daemon in link mode.
const (
	// EnvSecret holds the link secret.
	EnvSecret = "LYDIABOX_LINK_SECRET"
	// EnvSecretFile names a file that holds the link secret.
	EnvSecretFile = "LYDIABOX_LINK_SECRET_FILE"
)

// MinSecretLength is the shortest accepted link secret in bytes.
const MinSecretLength = 16

// HandshakeTimeout bounds the handshake after the WebSocket opens.
const HandshakeTimeout = 15 * time.Second

// headerTimeout bounds the read of the port header of a new stream.
const headerTimeout = 10 * time.Second

// ErrUnauthorized is returned when the peer does not prove the secret.
var ErrUnauthorized = errors.New("link: peer did not prove the link secret")

// CloseUnauthorized is the WebSocket close code for a failed handshake.
const CloseUnauthorized websocket.StatusCode = 4401

// ValidSecret checks the length of a link secret.
func ValidSecret(secret []byte) error {
	if len(secret) < MinSecretLength {
		return fmt.Errorf("link: the link secret must have at least %d bytes", MinSecretLength)
	}
	return nil
}

// SecretFromEnv reads the link secret from LYDIABOX_LINK_SECRET or the file
// named by LYDIABOX_LINK_SECRET_FILE, and removes both variables from the
// process environment so that child processes do not inherit the secret.
func SecretFromEnv() ([]byte, error) {
	v := os.Getenv(EnvSecret)
	file := os.Getenv(EnvSecretFile)
	_ = os.Unsetenv(EnvSecret)
	_ = os.Unsetenv(EnvSecretFile)
	if v == "" && file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("link: read %s: %w", EnvSecretFile, err)
		}
		v = strings.TrimSpace(string(b))
	}
	if v == "" {
		return nil, fmt.Errorf("link: set %s or %s", EnvSecret, EnvSecretFile)
	}
	secret := []byte(v)
	return secret, ValidSecret(secret)
}

// YamuxConfig returns the session configuration of the link. keepAlive is
// the ping interval (0 selects 10 s). A write or a ping answer that takes
// longer than three intervals ends the session, so a dead link is found in
// at most four intervals while a slow proxy still gets time for a large
// write.
func YamuxConfig(keepAlive time.Duration) *yamux.Config {
	if keepAlive <= 0 {
		keepAlive = 10 * time.Second
	}
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.Logger = nil
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = keepAlive
	cfg.ConnectionWriteTimeout = 3 * keepAlive
	// Each stream buffers at most this much unread data; the sender of a
	// slow stream waits without blocking other streams.
	cfg.MaxStreamWindowSize = 1 << 20
	cfg.AcceptBacklog = 256
	return cfg
}

type hello struct {
	V     int    `json:"v"`
	Nonce []byte `json:"nonce,omitempty"`
	Proof []byte `json:"proof,omitempty"`
	OK    bool   `json:"ok,omitempty"`
}

func proof(secret []byte, role string, hostNonce, guestNonce []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("lydiabox-link/v1/" + role + "\x00"))
	m.Write(hostNonce)
	m.Write(guestNonce)
	return m.Sum(nil)
}

func nonce() ([]byte, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return b, err
}

func readHello(ctx context.Context, c *websocket.Conn) (hello, error) {
	var h hello
	typ, b, err := c.Read(ctx)
	if err != nil {
		if websocket.CloseStatus(err) == CloseUnauthorized {
			return h, ErrUnauthorized
		}
		return h, fmt.Errorf("link: handshake: %w", err)
	}
	if typ != websocket.MessageText || len(b) > 4096 {
		return h, errors.New("link: handshake: unexpected message")
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return h, fmt.Errorf("link: handshake: %w", err)
	}
	if h.V != 1 {
		return h, fmt.Errorf("link: handshake: unsupported version %d", h.V)
	}
	return h, nil
}

func writeHello(ctx context.Context, c *websocket.Conn, h hello) error {
	h.V = 1
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, b)
}

// HostHandshake runs the host side of the handshake: it sends a nonce,
// checks the guest proof, and sends its own proof. The guest proves the
// secret first, so a wrong endpoint learns nothing about it.
func HostHandshake(ctx context.Context, c *websocket.Conn, secret []byte) error {
	hn, err := nonce()
	if err != nil {
		return err
	}
	if err := writeHello(ctx, c, hello{Nonce: hn}); err != nil {
		return fmt.Errorf("link: handshake: %w", err)
	}
	g, err := readHello(ctx, c)
	if err != nil {
		return err
	}
	if len(g.Nonce) != 32 || !hmac.Equal(g.Proof, proof(secret, "guest", hn, g.Nonce)) {
		c.Close(CloseUnauthorized, "unauthorized")
		return ErrUnauthorized
	}
	if err := writeHello(ctx, c, hello{Proof: proof(secret, "host", hn, g.Nonce)}); err != nil {
		return fmt.Errorf("link: handshake: %w", err)
	}
	ok, err := readHello(ctx, c)
	if err != nil {
		return err
	}
	if !ok.OK {
		return ErrUnauthorized
	}
	return nil
}

// GuestHandshake runs the guest side of the handshake.
func GuestHandshake(ctx context.Context, c *websocket.Conn, secret []byte) error {
	h, err := readHello(ctx, c)
	if err != nil {
		return err
	}
	if len(h.Nonce) != 32 {
		return errors.New("link: handshake: invalid host nonce")
	}
	gn, err := nonce()
	if err != nil {
		return err
	}
	if err := writeHello(ctx, c, hello{Nonce: gn, Proof: proof(secret, "guest", h.Nonce, gn)}); err != nil {
		return fmt.Errorf("link: handshake: %w", err)
	}
	p, err := readHello(ctx, c)
	if err != nil {
		return err
	}
	if !hmac.Equal(p.Proof, proof(secret, "host", h.Nonce, gn)) {
		c.Close(CloseUnauthorized, "unauthorized")
		return ErrUnauthorized
	}
	return writeHello(ctx, c, hello{OK: true})
}

// OpenStream opens a stream on sess to a port of the peer.
func OpenStream(sess *yamux.Session, port uint32) (net.Conn, error) {
	st, err := sess.OpenStream()
	if err != nil {
		return nil, err
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], port)
	if _, err := st.Write(b[:]); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

func readPort(c net.Conn) (uint32, error) {
	_ = c.SetReadDeadline(time.Now().Add(headerTimeout))
	var b [4]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		return 0, err
	}
	_ = c.SetReadDeadline(time.Time{})
	return binary.BigEndian.Uint32(b[:]), nil
}
