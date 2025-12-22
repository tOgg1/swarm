package ssh

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestBuildKnownHostsCallback_AcceptsKnownKey(t *testing.T) {
	signer := newTestSigner(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	line := knownhosts.Line([]string{"example.com"}, signer.PublicKey())
	if err := os.WriteFile(path, []byte(line+"\n"), 0644); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	callback, err := buildKnownHostsCallback([]string{path}, path, nil, zerolog.Nop())
	if err != nil {
		t.Fatalf("buildKnownHostsCallback: %v", err)
	}

	err = callback("example.com:22", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}, signer.PublicKey())
	if err != nil {
		t.Fatalf("callback returned error: %v", err)
	}
}

func TestBuildKnownHostsCallback_AddsUnknownKey(t *testing.T) {
	signer := newTestSigner(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	prompted := false
	prompt := func(hostname string, remote net.Addr, key xssh.PublicKey) (bool, error) {
		prompted = true
		if hostname == "" {
			t.Error("expected hostname to be provided to prompt")
		}
		return true, nil
	}

	callback, err := buildKnownHostsCallback([]string{path}, path, prompt, zerolog.Nop())
	if err != nil {
		t.Fatalf("buildKnownHostsCallback: %v", err)
	}

	err = callback("example.com:22", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}, signer.PublicKey())
	if err != nil {
		t.Fatalf("callback returned error: %v", err)
	}
	if !prompted {
		t.Error("expected prompt to be called")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	if !strings.Contains(string(data), "example.com") {
		t.Fatalf("expected known_hosts entry to include host: %s", string(data))
	}
}

func TestBuildKnownHostsCallback_RejectsUnknownKey(t *testing.T) {
	signer := newTestSigner(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	prompt := func(hostname string, remote net.Addr, key xssh.PublicKey) (bool, error) {
		return false, nil
	}

	callback, err := buildKnownHostsCallback([]string{path}, path, prompt, zerolog.Nop())
	if err != nil {
		t.Fatalf("buildKnownHostsCallback: %v", err)
	}

	err = callback("example.com:22", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}, signer.PublicKey())
	if !errors.Is(err, ErrHostKeyRejected) {
		t.Fatalf("expected ErrHostKeyRejected, got %v", err)
	}
}

func newTestSigner(t *testing.T) xssh.Signer {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := xssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return signer
}
