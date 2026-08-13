package firewalla

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func hostKey(t *testing.T) ssh.PublicKey {
	t.Helper()

	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return key
}

// An operator pastes whatever their tools printed, and ssh-keyscan prints the
// host in front of the key.
func TestParseHostKeyTakesEitherFormAnOperatorHasToHand(t *testing.T) {
	key := hostKey(t)
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))

	tests := []struct {
		name string
		line string
		ok   bool
	}{
		{name: "the bare key", line: authorized, ok: true},
		{name: "what ssh-keyscan prints", line: "router.example.com " + authorized, ok: true},
		{name: "with a comment", line: authorized + " router", ok: true},
		{name: "nothing at all"},
		{name: "not a key", line: "trust me"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := ParseHostKey(test.line)
			if !test.ok {
				if err == nil {
					t.Fatal("accepted something that is not a host key")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseHostKey: %v", err)
			}
			if string(parsed.Marshal()) != string(key.Marshal()) {
				t.Error("parsed a different key from the one written")
			}
		})
	}
}

// An empty pin is the dangerous case, so the refusal has to say why rather
// than reading like a missing optional field.
func TestParseHostKeyRefusesAnEmptyPinAndSaysWhy(t *testing.T) {
	_, err := ParseHostKey("")
	if err == nil {
		t.Fatal("an empty host key was accepted")
	}
	if !strings.Contains(err.Error(), "trusted") {
		t.Errorf("the refusal should say what an empty pin costs, got %q", err)
	}
}

// A mismatch has to name what the box presented, or a rebuilt box is a dead
// end: the operator cannot fix a pin they cannot read.
func TestPinnedNamesWhatTheBoxPresented(t *testing.T) {
	expected, presented := hostKey(t), hostKey(t)
	address := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}

	if err := pinned(expected)("router.example.com:22", address, expected); err != nil {
		t.Fatalf("the pinned key was refused: %v", err)
	}

	err := pinned(expected)("router.example.com:22", address, presented)
	if err == nil {
		t.Fatal("a different host key was accepted")
	}
	if !strings.Contains(err.Error(), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(presented)))) {
		t.Errorf("the refusal should quote the presented key, got %q", err)
	}
}

// A box holds three host keys and offers whichever it prefers, so a correct
// pin gets refused against a type nobody chose. Found against a real box:
// pinning its ed25519 key got an ECDSA one.
func TestTheClientAsksForTheKindOfKeyThatWasPinned(t *testing.T) {
	tests := []struct {
		name string
		key  ssh.PublicKey
		want []string
	}{
		{name: "ed25519", key: hostKey(t), want: []string{ssh.KeyAlgoED25519}},
		{
			name: "rsa",
			key:  rsaHostKey(t),
			want: []string{ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSA},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := hostKeyAlgorithms(test.key)
			if len(got) != len(test.want) {
				t.Fatalf("algorithms = %v, want %v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Errorf("algorithms[%d] = %q, want %q", i, got[i], test.want[i])
				}
			}
		})
	}

	config, err := Access{Password: "p", HostKey: authorized(t, hostKey(t))}.clientConfig()
	if err != nil {
		t.Fatalf("clientConfig: %v", err)
	}
	if len(config.HostKeyAlgorithms) != 1 || config.HostKeyAlgorithms[0] != ssh.KeyAlgoED25519 {
		t.Errorf("the client would accept %v, want only the pinned type", config.HostKeyAlgorithms)
	}
}

func rsaHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()

	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	key, err := ssh.NewPublicKey(&private.PublicKey)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return key
}

func authorized(t *testing.T, key ssh.PublicKey) string {
	t.Helper()
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

func TestAccess(t *testing.T) {
	t.Run("fills in the default port", func(t *testing.T) {
		if got := (Access{Host: "router.example.com"}).Address(); got != "router.example.com:22" {
			t.Errorf("Address() = %q, want the default port", got)
		}
	})

	t.Run("keeps a port that was set", func(t *testing.T) {
		if got := (Access{Host: "router.example.com", Port: 2222}).Address(); got != "router.example.com:2222" {
			t.Errorf("Address() = %q, want the configured port", got)
		}
	})

	t.Run("refuses to try with no credential", func(t *testing.T) {
		if _, err := (Access{Host: "router.example.com"}).authMethods(); err == nil {
			t.Fatal("built an auth method list out of nothing")
		}
	})

	t.Run("prefers a key over a password", func(t *testing.T) {
		methods, err := Access{Password: "p", PrivateKey: privateKey(t)}.authMethods()
		if err != nil {
			t.Fatalf("authMethods: %v", err)
		}
		if len(methods) != 2 {
			t.Fatalf("methods = %d, want the key and the password", len(methods))
		}
	})

	t.Run("says so when the key does not parse", func(t *testing.T) {
		if _, err := (Access{PrivateKey: "not a key"}).authMethods(); err == nil {
			t.Fatal("accepted a private key that is not one")
		}
	})
}

func privateKey(t *testing.T) string {
	t.Helper()

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}
