package provider

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lastgood"
)

// Image limits are learned from replies, not /wham/usage. Keep their last
// observation apart from the chat windows, so a restarted gateway and a
// terminal's magpie can show it until that window ends.
type imageLimitObservation struct {
	At    time.Time  `json:"at"`
	Limit CodexLimit `json:"limit"`
}

type imageLimits map[string]imageLimitObservation

// Bound forgotten accounts and invalidations too: a missing reset is an
// explicit unknown observation, so another process's older memory must
// not take its place merely because the account was removed from the file.
const imageLimitsKeep = 30 * 24 * time.Hour

func imageLimitsPath() string { return filepath.Join(filepath.Dir(Path()), "image-limits.json") }

var imageLimitsMu = filelock.Mutex{Path: func() string { return imageLimitsPath() + ".lock" }}

// A write that fails still leaves this process what it heard. The path
// keeps different settings folders (and tests' homes) apart.
var imageLimitsHeard struct {
	sync.Mutex
	path string
	m    imageLimits
}

func parseImageLimits(b []byte) (imageLimits, error) {
	var m imageLimits
	err := json.Unmarshal(b, &m)
	if err == nil && m == nil {
		err = errors.New("image limits must be an object")
	}
	return m, err
}

// imageLimitsRead is shared and never changed. A writer reads again under
// the file lock, even when a request holds filememo's previous reading.
func imageLimitsRead(path string, writing bool) (imageLimits, error) {
	if writing {
		return filememo.ReadNow("image limits", path, parseImageLimits)
	}
	return filememo.Read("image limits", path, parseImageLimits)
}

// NoteCodexImageLimit keeps the last image window an account's response
// headers told. Only one observation per account is kept; expired ones
// are discarded on the next write. A window without a reset supersedes
// the previous observation but cannot say anything about the allowance.
func NoteCodexImageLimit(agent, user string, limit CodexLimit) {
	if agent == "" || user == "" {
		return
	}
	path, key := imageLimitsPath(), agent+"/"+strings.ToLower(user)
	imageLimitsMu.Lock()
	defer imageLimitsMu.Unlock()
	now := time.Now()
	observation := imageLimitObservation{At: now, Limit: limit}
	imageLimitsHeard.Lock()
	if imageLimitsHeard.path != path {
		imageLimitsHeard.path, imageLimitsHeard.m = path, imageLimits{}
	}
	for k, v := range imageLimitsHeard.m {
		if now.Sub(v.At) > imageLimitsKeep || !v.Limit.Resets.IsZero() && !v.Limit.Resets.After(now) {
			delete(imageLimitsHeard.m, k)
		}
	}
	imageLimitsHeard.m[key] = observation
	imageLimitsHeard.Unlock()
	previous, err := imageLimitsRead(path, true)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		// Preserve a malformed file before replacing it; an unreadable
		// file is never taken for an empty set of other accounts.
		if lastgood.KeepUnparsed(path, func(b []byte) bool { _, err := parseImageLimits(b); return err == nil }) != nil {
			return
		}
		previous = nil
	}
	out := imageLimits{}
	for k, v := range previous {
		if now.Sub(v.At) <= imageLimitsKeep && (v.Limit.Resets.IsZero() || v.Limit.Resets.After(now)) {
			out[k] = v
		}
	}
	out[key] = observation
	b, err := json.Marshal(out)
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	_ = writeFileAtomic(path, b)
}

// ForgetCodexImageLimit drops an observation a later refusal contradicted.
// The refusal's rest remains the gateway's; another process must say
// unknown rather than resurrect the earlier "Available" reading.
func ForgetCodexImageLimit(agent, user string) {
	NoteCodexImageLimit(agent, user, CodexLimit{})
}

// CodexImageLimit is the newest image observation made by this or another
// magpie, while its window has not renewed. No reset, a failed read and no
// observation, and a window that ended are all unknown, never available.
func CodexImageLimit(agent, user string, now time.Time) (CodexLimit, bool) {
	path, key := imageLimitsPath(), agent+"/"+strings.ToLower(user)
	m, _ := imageLimitsRead(path, false)
	observation, ok := m[key]
	imageLimitsHeard.Lock()
	if imageLimitsHeard.path == path {
		if heard, exists := imageLimitsHeard.m[key]; exists && (!ok || heard.At.After(observation.At)) {
			observation, ok = heard, true
		}
	}
	imageLimitsHeard.Unlock()
	return observation.Limit, ok && now.Sub(observation.At) <= imageLimitsKeep && observation.Limit.Resets.After(now)
}
