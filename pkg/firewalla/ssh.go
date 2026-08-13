package firewalla

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// redisAddress is where the box's Redis listens. It is bound to loopback, so
// the tunnel is not an optimisation: there is no other way in.
const redisAddress = "127.0.0.1:6379"

// dialTimeout bounds the TCP connect and the SSH handshake.
const dialTimeout = 15 * time.Second

// Access is everything needed to reach one box over SSH.
type Access struct {
	// Host is where the box answers SSH.
	Host string

	// Port defaults to 22.
	Port int

	// User is the box's shell account.
	User string

	// Password authenticates when no private key is given.
	Password string

	// PrivateKey is a PEM encoded key, used in preference to a password.
	PrivateKey string

	// KeyPassphrase decrypts PrivateKey when it is encrypted.
	KeyPassphrase string

	// HostKey is the key the box must present, in the one line form
	// ssh-keyscan prints. Without it, anything answering would be trusted.
	HostKey string
}

// Open returns a connection to the box's Redis. It is a function so the
// inventory can be tested without an SSH server.
type Open func(ctx context.Context) (net.Conn, error)

// Address is where SSH is expected, with the default port filled in.
func (a Access) Address() string {
	port := a.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(a.Host, strconv.Itoa(port))
}

// Tunnel dials the box and opens a channel to its Redis. Closing the returned
// connection closes the SSH client with it.
func (a Access) Tunnel(ctx context.Context) (net.Conn, error) {
	config, err := a.clientConfig()
	if err != nil {
		return nil, err
	}

	dialer := net.Dialer{Timeout: dialTimeout}
	socket, err := dialer.DialContext(ctx, "tcp", a.Address())
	if err != nil {
		return nil, fmt.Errorf("firewalla: reach the box over SSH: %w", err)
	}

	if err := socket.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
		_ = socket.Close()
		return nil, err
	}

	server, channels, requests, err := ssh.NewClientConn(socket, a.Address(), config)
	if err != nil {
		_ = socket.Close()
		return nil, fmt.Errorf("firewalla: authenticate to the box: %w", err)
	}
	if err := socket.SetDeadline(time.Time{}); err != nil {
		_ = server.Close()
		return nil, err
	}

	client := ssh.NewClient(server, channels, requests)
	redis, err := client.DialContext(ctx, "tcp", redisAddress)
	if err != nil {
		_ = client.Close()

		// The box has to permit forwarding for this to work at all, and an
		// operator reading "channel open failed" would have no idea why.
		return nil, fmt.Errorf("firewalla: open a channel to Redis on the box, "+
			"which needs AllowTcpForwarding in its sshd: %w", err)
	}

	return tunnel{Conn: redis, client: client}, nil
}

// tunnel ties the Redis channel's lifetime to the SSH client's, so closing one
// connection does not leave a session on the router.
type tunnel struct {
	net.Conn
	client *ssh.Client
}

func (t tunnel) Close() error {
	err := t.Conn.Close()
	if closed := t.client.Close(); err == nil {
		err = closed
	}
	return err
}

func (a Access) clientConfig() (*ssh.ClientConfig, error) {
	expected, err := ParseHostKey(a.HostKey)
	if err != nil {
		return nil, err
	}

	methods, err := a.authMethods()
	if err != nil {
		return nil, err
	}

	user := a.User
	if user == "" {
		user = "pi"
	}

	return &ssh.ClientConfig{
		User:              user,
		Auth:              methods,
		HostKeyCallback:   pinned(expected),
		HostKeyAlgorithms: hostKeyAlgorithms(expected),
		Timeout:           dialTimeout,
	}, nil
}

// hostKeyAlgorithms asks the box for the kind of key that was pinned. A box
// holds several, and without this it offers whichever it prefers, so a
// correct pin fails against a key of the wrong type.
func hostKeyAlgorithms(key ssh.PublicKey) []string {
	if key.Type() == ssh.KeyAlgoRSA {
		// One RSA key answers to three signature algorithms.
		return []string{ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSA}
	}
	return []string{key.Type()}
}

func (a Access) authMethods() ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if a.PrivateKey != "" {
		signer, err := a.signer()
		if err != nil {
			return nil, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if a.Password != "" {
		methods = append(methods, ssh.Password(a.Password))
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("firewalla: no way to authenticate, set a password or a private key")
	}
	return methods, nil
}

func (a Access) signer() (ssh.Signer, error) {
	key := []byte(a.PrivateKey)
	if a.KeyPassphrase == "" {
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("firewalla: read the private key: %w", err)
		}
		return signer, nil
	}

	signer, err := ssh.ParsePrivateKeyWithPassphrase(key, []byte(a.KeyPassphrase))
	if err != nil {
		return nil, fmt.Errorf("firewalla: decrypt the private key: %w", err)
	}
	return signer, nil
}

// ParseHostKey reads the key an operator pinned, in either the bare form or
// the host prefixed form ssh-keyscan prints.
func ParseHostKey(line string) (ssh.PublicKey, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, fmt.Errorf("firewalla: no host key pinned, so anything answering would be trusted")
	}

	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err == nil {
		return key, nil
	}

	if _, rest, found := strings.Cut(line, " "); found {
		if key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(rest)); err == nil {
			return key, nil
		}
	}
	return nil, fmt.Errorf("firewalla: that is not an SSH host key: %w", err)
}

// pinned refuses anything but the key the operator pinned, and names what was
// presented so a first run or a rebuilt box has something to paste.
func pinned(expected ssh.PublicKey) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, presented ssh.PublicKey) error {
		if bytes.Equal(expected.Marshal(), presented.Marshal()) {
			return nil
		}
		return fmt.Errorf("firewalla: the box presented %q, which is not the pinned host key",
			strings.TrimSpace(string(ssh.MarshalAuthorizedKey(presented))))
	}
}
