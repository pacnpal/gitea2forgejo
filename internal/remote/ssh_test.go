package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

func genTestKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(priv.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func TestHostKeyCallback_rejectsWhenUnconfigured(t *testing.T) {
	_, err := hostKeyCallback(&config.SSH{})
	if err == nil {
		t.Fatal("expected error when neither known_hosts nor fingerprint configured")
	}
}

func TestHostKeyCallback_fingerprintMatch(t *testing.T) {
	key := genTestKey(t)
	cb, err := hostKeyCallback(&config.SSH{HostKeyFingerprint: ssh.FingerprintSHA256(key)})
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("host.example.com:22", &net.TCPAddr{}, key); err != nil {
		t.Errorf("matching fingerprint rejected: %v", err)
	}
}

func TestHostKeyCallback_fingerprintMismatch(t *testing.T) {
	key1 := genTestKey(t)
	key2 := genTestKey(t)
	cb, err := hostKeyCallback(&config.SSH{HostKeyFingerprint: ssh.FingerprintSHA256(key1)})
	if err != nil {
		t.Fatal(err)
	}
	err = cb("host.example.com:22", &net.TCPAddr{}, key2)
	if err == nil {
		t.Fatal("expected rejection for mismatched fingerprint")
	}
	if !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHostKeyCallback_knownHostsMissing(t *testing.T) {
	_, err := hostKeyCallback(&config.SSH{
		Host:       "bogus.example.com",
		KnownHosts: filepath.Join(t.TempDir(), "nonexistent"),
	})
	if err == nil {
		t.Fatal("expected error when known_hosts file is missing")
	}
	if !strings.Contains(err.Error(), "ssh-keyscan") {
		t.Errorf("error should suggest ssh-keyscan remediation: %v", err)
	}
}

func TestHostKeyCallback_knownHostsPresent(t *testing.T) {
	key := genTestKey(t)
	dir := t.TempDir()
	khPath := filepath.Join(dir, "known_hosts")
	line := "[example.com]:22 " + string(ssh.MarshalAuthorizedKey(key))
	if err := os.WriteFile(khPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	cb, err := hostKeyCallback(&config.SSH{Host: "example.com", KnownHosts: khPath})
	if err != nil {
		t.Fatal(err)
	}
	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}
	if err := cb("[example.com]:22", addr, key); err != nil {
		t.Errorf("known host rejected: %v", err)
	}
}
