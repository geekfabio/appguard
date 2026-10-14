// Package update implements AppGuard agent self-update from the controller.
//
// The controller and the agents run the same appguard.exe, so the controller
// offers its own binary. The agent downloads it next to the running
// executable, verifies it, swaps the files by renaming (Windows allows
// renaming a running executable) and restarts the service. The new binary is
// on trial until it has stayed healthy for a while; if it keeps failing to
// start, the previous binary is restored and the new one is never retried.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// MaxTrialStarts is how many times an updated binary may start without
	// becoming healthy before it is rolled back.
	MaxTrialStarts = 3
	// HealthyAfter is how long an updated binary must run while talking to
	// the controller before the update is committed.
	HealthyAfter = 2 * time.Minute
	// retryAfter limits how often a failed download/install is retried.
	retryAfter = 10 * time.Minute
)

// Manifest describes the binary the controller offers.
type Manifest struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// Result is an outcome to be reported to the controller once.
type Result struct {
	Action  string `json:"action"`
	Version string `json:"version"`
	Detail  string `json:"detail"`
}

type pending struct {
	FromVersion string    `json:"fromVersion"`
	ToVersion   string    `json:"toVersion"`
	ToSHA256    string    `json:"toSha256"`
	Starts      int       `json:"starts"`
	InstalledAt time.Time `json:"installedAt"`
}

type state struct {
	Pending    *pending `json:"pending,omitempty"`
	BadSHA256  []string `json:"badSha256,omitempty"`
	LastResult *Result  `json:"lastResult,omitempty"`
}

type Updater struct {
	exe       string
	version   string
	sha       string
	statePath string
	startedAt time.Time

	// verify checks that a downloaded binary runs and reports the expected
	// version. Replaced in tests.
	verify func(ctx context.Context, path, version string) error

	mu         sync.Mutex
	st         state
	busy       bool
	lastFailed map[string]time.Time
}

// New returns an updater for the running executable.
func New(version string) (*Updater, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return NewAt(exe, version)
}

// NewAt returns an updater for the executable at exe.
func NewAt(exe, version string) (*Updater, error) {
	sum, _, err := FileSHA256(exe)
	if err != nil {
		return nil, fmt.Errorf("hash executable: %w", err)
	}
	u := &Updater{
		exe:        exe,
		version:    version,
		sha:        sum,
		statePath:  strings.TrimSuffix(exe, filepath.Ext(exe)) + ".update.json",
		startedAt:  time.Now(),
		verify:     verifyBinary,
		lastFailed: make(map[string]time.Time),
	}
	if b, err := os.ReadFile(u.statePath); err == nil {
		_ = json.Unmarshal(b, &u.st)
	}
	return u, nil
}

// SHA256 is the hash of the running binary.
func (u *Updater) SHA256() string { return u.sha }

// Startup must be called when the service starts. It counts trial starts of
// a freshly installed binary and rolls back after too many. It returns true
// when a rollback was done and the service must be restarted to run the
// previous binary.
func (u *Updater) Startup() (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	p := u.st.Pending
	if p == nil {
		return false, nil
	}
	if p.ToSHA256 != u.sha {
		// Running something other than the binary on trial (manual
		// replacement or an earlier rollback): nothing to supervise.
		u.st.Pending = nil
		return false, u.saveLocked()
	}

	p.Starts++
	if p.Starts <= MaxTrialStarts {
		return false, u.saveLocked()
	}
	return true, u.rollbackLocked(fmt.Sprintf("versão %s não ficou estável após %d arranques", p.ToVersion, MaxTrialStarts))
}

func (u *Updater) rollbackLocked(reason string) error {
	p := u.st.Pending
	u.st.Pending = nil
	u.st.BadSHA256 = appendBounded(u.st.BadSHA256, p.ToSHA256, 20)

	old := u.exe + ".old"
	if _, err := os.Stat(old); err != nil {
		u.st.LastResult = &Result{Action: "agent-update-failed", Version: p.ToVersion,
			Detail: reason + "; executável anterior não encontrado para rollback"}
		_ = u.saveLocked()
		return fmt.Errorf("rollback: %w", err)
	}

	bad := u.exe + ".bad"
	_ = os.Remove(bad)
	if err := os.Rename(u.exe, bad); err != nil {
		_ = u.saveLocked()
		return fmt.Errorf("rollback: %w", err)
	}
	if err := os.Rename(old, u.exe); err != nil {
		_ = os.Rename(bad, u.exe)
		_ = u.saveLocked()
		return fmt.Errorf("rollback: %w", err)
	}
	u.st.LastResult = &Result{Action: "agent-update-rolled-back", Version: p.FromVersion,
		Detail: reason + "; reposta a versão " + p.FromVersion}
	return u.saveLocked()
}

// ConfirmHealthy is called after each successful contact with the
// controller and commits a pending update once the binary has been running
// for HealthyAfter.
func (u *Updater) ConfirmHealthy() {
	u.mu.Lock()
	defer u.mu.Unlock()

	p := u.st.Pending
	if p == nil || p.ToSHA256 != u.sha || time.Since(u.startedAt) < HealthyAfter {
		return
	}
	u.st.Pending = nil
	u.st.LastResult = &Result{Action: "agent-update-installed", Version: p.ToVersion,
		Detail: fmt.Sprintf("actualizado de %s para %s", p.FromVersion, p.ToVersion)}
	_ = os.Remove(u.exe + ".old")
	_ = os.Remove(u.exe + ".bad")
	_ = u.saveLocked()
}

// TakeResult returns the outcome to report, if any, and clears it.
func (u *Updater) TakeResult() *Result {
	u.mu.Lock()
	defer u.mu.Unlock()
	r := u.st.LastResult
	if r != nil {
		u.st.LastResult = nil
		_ = u.saveLocked()
	}
	return r
}

// Claim reports whether the offered binary should be installed now and, if
// so, reserves the updater; the caller must then call Install.
func (u *Updater) Claim(m Manifest) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if m.SHA256 == "" || strings.EqualFold(m.SHA256, u.sha) || u.busy || u.st.Pending != nil {
		return false
	}
	if slices.Contains(u.st.BadSHA256, strings.ToLower(m.SHA256)) {
		return false
	}
	if time.Since(u.lastFailed[m.SHA256]) < retryAfter {
		return false
	}
	u.busy = true
	return true
}

// Install downloads the offered binary from url, verifies it and swaps it in
// place of the running executable. It must follow a successful Claim. On
// success the caller must restart the service so the new binary runs.
func (u *Updater) Install(ctx context.Context, client *http.Client, url string, m Manifest) (err error) {
	u.mu.Lock()
	claimed := u.busy
	u.mu.Unlock()
	if !claimed {
		return errors.New("Install chamado sem Claim")
	}

	defer func() {
		u.mu.Lock()
		u.busy = false
		if err != nil {
			u.lastFailed[m.SHA256] = time.Now()
		}
		u.mu.Unlock()
	}()

	want := strings.ToLower(m.SHA256)
	// Keep the .exe extension so Windows will run it for verification.
	ext := filepath.Ext(u.exe)
	next := strings.TrimSuffix(u.exe, ext) + ".new" + ext
	if err := download(ctx, client, url, next, want, m.Size); err != nil {
		_ = os.Remove(next)
		return err
	}
	if err := u.verify(ctx, next, m.Version); err != nil {
		_ = os.Remove(next)
		return fmt.Errorf("binário descarregado não arranca: %w", err)
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	u.st.Pending = &pending{
		FromVersion: u.version,
		ToVersion:   m.Version,
		ToSHA256:    want,
		InstalledAt: time.Now(),
	}
	if err := u.saveLocked(); err != nil {
		u.st.Pending = nil
		_ = os.Remove(next)
		return err
	}

	old := u.exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(u.exe, old); err != nil {
		u.st.Pending = nil
		_ = u.saveLocked()
		_ = os.Remove(next)
		return fmt.Errorf("mover executável actual: %w", err)
	}
	if err := os.Rename(next, u.exe); err != nil {
		_ = os.Rename(old, u.exe)
		u.st.Pending = nil
		_ = u.saveLocked()
		_ = os.Remove(next)
		return fmt.Errorf("instalar novo executável: %w", err)
	}
	return nil
}

func download(ctx context.Context, client *http.Client, url, dest, wantSHA string, wantSize int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("descarregar: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("descarregar: HTTP %d", resp.StatusCode)
	}

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("descarregar: %w", err)
	}
	if wantSize > 0 && n != wantSize {
		return fmt.Errorf("tamanho inesperado: %d bytes, esperado %d", n, wantSize)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
		return fmt.Errorf("SHA-256 não confere: %s, esperado %s", got, wantSHA)
	}
	return nil
}

func verifyBinary(ctx context.Context, path, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	if version != "" && !strings.Contains(string(out), version) {
		return fmt.Errorf("reporta %q, esperado %s", strings.TrimSpace(string(out)), version)
	}
	return nil
}

// FileSHA256 returns the lowercase hex SHA-256 and size of a file.
func FileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func (u *Updater) saveLocked() error {
	b, err := json.MarshalIndent(u.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := u.statePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, u.statePath)
}

func appendBounded(list []string, v string, max int) []string {
	if slices.Contains(list, v) {
		return list
	}
	list = append(list, v)
	if len(list) > max {
		list = list[len(list)-max:]
	}
	return list
}
