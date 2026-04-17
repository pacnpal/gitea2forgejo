// Package remote provides SSH exec + SFTP file ops against source/target hosts.
package remote

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

type Client struct {
	ssh  *ssh.Client
	sftp *sftp.Client
	addr string
}

// Dial opens an SSH connection with the settings from config.SSH.
// The caller must Close() when done.
//
// Host key verification order:
//  1. If HostKeyFingerprint is set, the advertised key's SHA256 fingerprint
//     must match.
//  2. Otherwise KnownHosts (defaulting to ~/.ssh/known_hosts) is consulted
//     via golang.org/x/crypto/ssh/knownhosts.
//
// There is intentionally no "trust on first use" fallback — if neither
// mechanism can authenticate the host, Dial returns an error.
func Dial(c *config.SSH) (*Client, error) {
	if c == nil {
		return nil, fmt.Errorf("ssh block is nil")
	}
	key, err := os.ReadFile(c.Key)
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w", c.Key, err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("parse key: %w", err)
	}
	hkCallback, err := hostKeyCallback(c)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            c.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hkCallback,
		Timeout:         15 * time.Second,
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	sshClient, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("sftp new: %w", err)
	}
	return &Client{ssh: sshClient, sftp: sftpClient, addr: addr}, nil
}

// hostKeyCallback returns an ssh.HostKeyCallback that verifies the remote
// host key against an explicit fingerprint (if configured) or a known_hosts
// file. Returns an error if neither source is usable — never returns
// ssh.InsecureIgnoreHostKey().
func hostKeyCallback(c *config.SSH) (ssh.HostKeyCallback, error) {
	// Pin 1: explicit fingerprint. Independent of any file on disk.
	if fp := strings.TrimSpace(c.HostKeyFingerprint); fp != "" {
		want := fp
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			got := ssh.FingerprintSHA256(key)
			if got != want {
				return fmt.Errorf("host key fingerprint mismatch for %s: expected %s, got %s",
					hostname, want, got)
			}
			return nil
		}, nil
	}
	// Pin 2: known_hosts.
	if c.KnownHosts == "" {
		return nil, fmt.Errorf("host key verification not configured: set ssh.known_hosts or ssh.host_key_fingerprint")
	}
	if _, err := os.Stat(c.KnownHosts); err != nil {
		return nil, fmt.Errorf("known_hosts file %s: %w (add the host via `ssh-keyscan -H %s >> %s` or configure ssh.host_key_fingerprint)",
			c.KnownHosts, err, c.Host, c.KnownHosts)
	}
	cb, err := knownhosts.New(c.KnownHosts)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts %s: %w", c.KnownHosts, err)
	}
	// Wrap with a friendlier error for the common "host not in file" case.
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := cb(hostname, remote, key)
		if err == nil {
			return nil
		}
		var kerr *knownhosts.KeyError
		if errors.As(err, &kerr) && len(kerr.Want) == 0 {
			return fmt.Errorf("%s is not in %s; add it via `ssh-keyscan -H %s >> %s` or set ssh.host_key_fingerprint",
				hostname, c.KnownHosts, c.Host, c.KnownHosts)
		}
		return err
	}, nil
}

// Run executes cmd and returns combined stdout+stderr.
func (c *Client) Run(cmd string) ([]byte, error) {
	sess, err := c.ssh.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	var buf bytes.Buffer
	sess.Stdout = &buf
	sess.Stderr = &buf
	if err := sess.Run(cmd); err != nil {
		return buf.Bytes(), fmt.Errorf("remote %q: %w: %s", cmd, err, buf.String())
	}
	return buf.Bytes(), nil
}

// RunStream executes cmd streaming stdout/stderr to w.
func (c *Client) RunStream(cmd string, w io.Writer) error {
	sess, err := c.ssh.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdout = w
	sess.Stderr = w
	return sess.Run(cmd)
}

// ReadFile reads a remote file fully into memory. Use only for small files
// (app.ini, etc.); use OpenRemote for large files.
func (c *Client) ReadFile(path string) ([]byte, error) {
	f, err := c.sftp.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// FetchFile downloads remotePath to localPath.
func (c *Client) FetchFile(remotePath, localPath string) error {
	src, err := c.sftp.Open(remotePath)
	if err != nil {
		return fmt.Errorf("open remote %s: %w", remotePath, err)
	}
	defer src.Close()
	dst, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("create local %s: %w", localPath, err)
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return nil
}

// WriteFile writes data to a remote path with the given mode, creating the
// file if needed (truncates existing content).
func (c *Client) WriteFile(path string, data []byte, mode os.FileMode) error {
	f, err := c.sftp.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("open remote %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write remote %s: %w", path, err)
	}
	if err := c.sftp.Chmod(path, mode); err != nil {
		return fmt.Errorf("chmod remote %s: %w", path, err)
	}
	return nil
}

// Exists returns true if path exists on the remote.
func (c *Client) Exists(path string) (bool, error) {
	_, err := c.sftp.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// DiskFreeBytes returns free bytes on the filesystem holding path.
// Relies on `df -B1 --output=avail PATH | tail -1`.
func (c *Client) DiskFreeBytes(path string) (uint64, error) {
	out, err := c.Run("df -B1 --output=avail " + shellQuote(path) + " | tail -1")
	if err != nil {
		return 0, err
	}
	var n uint64
	if _, err := fmt.Sscanf(string(bytes.TrimSpace(out)), "%d", &n); err != nil {
		return 0, fmt.Errorf("parse df output %q: %w", out, err)
	}
	return n, nil
}

// DirSizeBytes returns the size of a directory tree on the remote.
// Relies on `du -sb PATH`.
func (c *Client) DirSizeBytes(path string) (uint64, error) {
	out, err := c.Run("du -sb " + shellQuote(path) + " | awk '{print $1}'")
	if err != nil {
		return 0, err
	}
	var n uint64
	if _, err := fmt.Sscanf(string(bytes.TrimSpace(out)), "%d", &n); err != nil {
		return 0, fmt.Errorf("parse du output %q: %w", out, err)
	}
	return n, nil
}

func (c *Client) Close() error {
	if c.sftp != nil {
		c.sftp.Close()
	}
	if c.ssh != nil {
		return c.ssh.Close()
	}
	return nil
}

// shellQuote single-quotes s for safe substitution into a remote shell cmd.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
