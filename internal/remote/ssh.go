// Package remote provides SSH exec + SFTP file ops against source/target hosts.
package remote

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
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
	auth, err := buildAuth(c)
	if err != nil {
		return nil, err
	}
	hkCallback, err := hostKeyCallback(c)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            c.User,
		Auth:            auth,
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

// buildAuth returns the ssh.AuthMethods to try, in order:
//
//  1. If c.Key is set and readable, the key file.
//  2. If SSH_AUTH_SOCK is exported, an ssh-agent backed method.
//
// At least one must succeed; otherwise the connection will never authenticate
// and we'd rather fail early with a clear error.
func buildAuth(c *config.SSH) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	var probeErrs []string

	// Key file (if specified and present).
	if c.Key != "" {
		data, err := os.ReadFile(c.Key)
		switch {
		case err == nil:
			signer, err := ssh.ParsePrivateKey(data)
			if err != nil {
				return nil, fmt.Errorf("parse key %s: %w (is it password-protected? agent-forward it instead)", c.Key, err)
			}
			methods = append(methods, ssh.PublicKeys(signer))
		case os.IsNotExist(err):
			probeErrs = append(probeErrs, c.Key+" not found")
		default:
			return nil, fmt.Errorf("read key %s: %w", c.Key, err)
		}
	}

	// ssh-agent fallback.
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err != nil {
			probeErrs = append(probeErrs, "agent at SSH_AUTH_SOCK unreachable: "+err.Error())
		} else {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	} else {
		probeErrs = append(probeErrs, "no SSH_AUTH_SOCK in environment")
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf(
			"no usable SSH auth: %s. "+
				"Set ssh.key to an existing private key OR start an ssh-agent with `ssh-add`",
			strings.Join(probeErrs, "; "))
	}
	return methods, nil
}

// ErrHostUnknown signals that the remote host is not present in the
// known_hosts file. It is safe for callers to run `ssh-keyscan` and
// append the result; the server is not presenting a key that conflicts
// with a previously-recorded one.
//
// A MISMATCHED host key (i.e. a MITM or server re-key) returns a
// different error and is NOT wrapped by ErrHostUnknown — callers must
// not auto-trust in that case.
var ErrHostUnknown = errors.New("host not in known_hosts")

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
			// Wrap ErrHostUnknown so callers can errors.Is() it and
			// decide whether to auto-scan.
			return fmt.Errorf("%s is not in %s: %w",
				hostname, c.KnownHosts, ErrHostUnknown)
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

// FetchFile downloads remotePath to localPath and returns how the
// transfer was satisfied. When the result is ResultSymlinked the caller
// MUST NOT delete the source file while localPath is still in use, or
// the symlink becomes dangling.
func (c *Client) FetchFile(remotePath, localPath string) (LocalFetchResult, error) {
	if fi, err := os.Stat(remotePath); err == nil && !fi.IsDir() {
		return localFetch(remotePath, localPath, fi)
	}
	src, err := c.sftp.Open(remotePath)
	if err != nil {
		return ResultRemote, fmt.Errorf("open remote %s: %w", remotePath, err)
	}
	defer src.Close()
	var total int64
	if st, err := c.sftp.Stat(remotePath); err == nil {
		total = st.Size()
	}
	dst, err := os.Create(localPath)
	if err != nil {
		return ResultRemote, fmt.Errorf("create local %s: %w", localPath, err)
	}
	defer dst.Close()
	pr := newProgressReporter(remotePath, total)
	defer pr.done()
	if _, err := io.Copy(io.MultiWriter(dst, pr), src); err != nil {
		return ResultRemote, fmt.Errorf("copy: %w", err)
	}
	return ResultRemote, nil
}

// LocalFetchResult describes how FetchFile satisfied a same-host transfer.
// Callers use ResultSymlinked / ResultHardLinked to decide whether
// deleting the source file is safe — hard links keep the inode alive;
// symlinks break.
type LocalFetchResult int

const (
	ResultCopied     LocalFetchResult = iota // bytes were actually moved
	ResultHardLinked                         // new directory entry, same inode
	ResultSymlinked                          // new symlink; source must NOT be deleted
	ResultRemote                             // SFTP was used (truly remote source)
)

// localFetch is the fast path when the "remote" file is visible on the
// local filesystem. Four tiers, each faster than the SFTP path:
//
//  1. os.Link — same filesystem, O(1). Zero bytes transferred. Source
//     can safely be deleted afterwards (inode survives via our link).
//  2. os.Symlink — cross filesystem, O(1). Zero bytes transferred.
//     The source file is kept in place and must NOT be cleaned up
//     while the symlink is in use. Caller is informed via the returned
//     LocalFetchResult so downstream cleanup can be suppressed.
//  3. cp --reflink=auto — same-fs reflink on XFS/btrfs/ZFS (instant
//     COW clone); else kernel-optimized copy.
//  4. Native io.Copy with a PURE io.Reader / io.Writer pair so Go's
//     os.File.ReadFrom can engage sendfile(2) on Linux.
func localFetch(src, dst string, fi os.FileInfo) (LocalFetchResult, error) {
	_ = os.Remove(dst) // Link/Symlink/Create fail if dst exists.

	// Tier 1: hard link.
	if err := os.Link(src, dst); err == nil {
		fmt.Fprintf(os.Stderr, "  local fast-path: hard-linked %s (%d MiB, zero copy)\n",
			src, fi.Size()/(1<<20))
		return ResultHardLinked, nil
	}

	// Tier 2: cross-filesystem symlink. Avoids any byte transfer when
	// hard link fails because of EXDEV (common on Unraid shfs ↔ XFS
	// disks, btrfs subvolumes, etc.). Caller MUST NOT delete the source.
	if err := os.Symlink(src, dst); err == nil {
		fmt.Fprintf(os.Stderr,
			"  local fast-path: symlinked → %s (%d MiB, zero copy)\n",
			src, fi.Size()/(1<<20))
		return ResultSymlinked, nil
	}

	// Tier 3: cp --reflink=auto.
	if cpPath, err := exec.LookPath("cp"); err == nil {
		fmt.Fprintf(os.Stderr, "  local fast-path: cp --reflink=auto %s (%d MiB)\n",
			src, fi.Size()/(1<<20))
		cmd := exec.Command(cpPath, "--reflink=auto", "--preserve=timestamps", src, dst)
		cmd.Stderr = os.Stderr
		stop := startSizePoller(dst, fi.Size())
		err := cmd.Run()
		close(stop)
		if err == nil {
			return ResultCopied, nil
		}
		fmt.Fprintf(os.Stderr, "  cp failed (%v); falling back to io.Copy\n", err)
	}

	// Tier 4: pure Go copy with sendfile(2).
	in, err := os.Open(src)
	if err != nil {
		return ResultCopied, fmt.Errorf("open local %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return ResultCopied, fmt.Errorf("create local %s: %w", dst, err)
	}
	defer out.Close()

	fmt.Fprintf(os.Stderr, "  local fast-path: io.Copy with sendfile %s (%d MiB)\n",
		src, fi.Size()/(1<<20))
	stop := startSizePoller(dst, fi.Size())
	defer close(stop)
	if _, err := io.Copy(out, in); err != nil {
		return ResultCopied, fmt.Errorf("local copy: %w", err)
	}
	return ResultCopied, nil
}

// startSizePoller logs destination file size every 5 seconds until close()d.
// Used when the copy engine is out of our reach (shelled cp) or when
// wrapping the writer would disable sendfile.
func startSizePoller(dst string, total int64) chan<- struct{} {
	stop := make(chan struct{})
	go func() {
		start := time.Now()
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				fi, err := os.Stat(dst)
				if err != nil {
					continue
				}
				seen := fi.Size()
				rate := float64(seen) / time.Since(start).Seconds() / (1 << 20)
				if total > 0 {
					pct := float64(seen) * 100 / float64(total)
					fmt.Fprintf(os.Stderr, "  copy progress: %.1f%% (%d / %d MiB, avg %.1f MiB/s)\n",
						pct, seen/(1<<20), total/(1<<20), rate)
				} else {
					fmt.Fprintf(os.Stderr, "  copy progress: %d MiB (avg %.1f MiB/s)\n",
						seen/(1<<20), rate)
				}
			}
		}
	}()
	return stop
}

// progressReporter is a minimal writer that counts bytes and periodically
// logs "fetched X / Y (Z MB/s)". Writes are cheap (just a counter); the
// logging lives in a goroutine started by newProgressReporter.
type progressReporter struct {
	path  string
	total int64
	seen  int64
	start time.Time
	stop  chan struct{}
}

func newProgressReporter(path string, total int64) *progressReporter {
	pr := &progressReporter{
		path:  path,
		total: total,
		start: time.Now(),
		stop:  make(chan struct{}),
	}
	go pr.loop()
	return pr
}

func (p *progressReporter) Write(b []byte) (int, error) {
	p.seen += int64(len(b))
	return len(b), nil
}

func (p *progressReporter) loop() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.emit()
		}
	}
}

func (p *progressReporter) emit() {
	seen := p.seen
	elapsed := time.Since(p.start).Seconds()
	rate := float64(seen) / elapsed / (1 << 20) // MiB/s
	if p.total > 0 {
		pct := float64(seen) * 100 / float64(p.total)
		fmt.Fprintf(os.Stderr, "  fetch %s: %.1f%% (%d / %d MB, %.1f MiB/s)\n",
			p.path, pct, seen/(1<<20), p.total/(1<<20), rate)
	} else {
		fmt.Fprintf(os.Stderr, "  fetch %s: %d MB (%.1f MiB/s)\n",
			p.path, seen/(1<<20), rate)
	}
}

func (p *progressReporter) done() {
	close(p.stop)
	// Final summary line.
	p.emit()
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
