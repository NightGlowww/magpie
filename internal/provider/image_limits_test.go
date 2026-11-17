package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/proc"
)

// Restart without forgetting the file: a terminal and a restarted app
// have neither the gateway's memory nor a need to draw another image.
func forgetImageLimitMemory() {
	imageLimitsHeard.Lock()
	imageLimitsHeard.path, imageLimitsHeard.m = "", nil
	imageLimitsHeard.Unlock()
	filememo.Forget()
}

func TestCodexImageLimitsSurviveRestart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()
	reset := now.Add(12 * time.Hour).Round(time.Second)
	for _, used := range []float64{0, 100} {
		NoteCodexImageLimit("codex", "Me@example.com", CodexLimit{Used: used, Span: 24 * time.Hour, Resets: reset})
		forgetImageLimitMemory()
		limit, ok := CodexImageLimit("codex", "me@example.com", now)
		if !ok || limit.Used != used || limit.Span != 24*time.Hour || !limit.Resets.Equal(reset) {
			t.Fatalf("after a restart: %+v, known %v; want used %v and reset %v", limit, ok, used, reset)
		}
		cards := WithImageLane([]SubscriptionQuota{{Provider: "codex", User: "me@example.com"}}, now)
		want := "Available"
		if used == 100 {
			want = "Used up"
		}
		if w := cards[0].Windows[0]; w.Display != want || w.ResetsAt == nil || !w.ResetsAt.Equal(reset) || !w.Text || w.Used != 0 {
			t.Fatalf("after a restart the card = %+v, want %s until %v without a share", w, want, reset)
		}
	}
	if _, ok := CodexImageLimit("codex", "me@example.com", reset); ok {
		t.Fatal("an ended image window was reported after its reset")
	}
	for _, who := range [][2]string{{"codex", "other@example.com"}, {"chatgpt-api", "me@example.com"}} {
		if _, ok := CodexImageLimit(who[0], who[1], now); ok {
			t.Fatalf("%v took another account's image window", who)
		}
	}
	first := os.Getenv("XDG_CONFIG_HOME")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, ok := CodexImageLimit("codex", "me@example.com", now); ok {
		t.Fatal("another settings folder took this one's image window")
	}
	t.Setenv("XDG_CONFIG_HOME", first)
	if _, ok := CodexImageLimit("codex", "me@example.com", now); !ok {
		t.Fatal("returning to the settings folder lost its image window")
	}
}

// A separate process writes two accounts after the reader already heard
// one of them. Seeing only this process's memory, or rewriting the file
// from a stale snapshot, loses either the update or the untouched account.
func TestCodexImageLimitsOtherProcess(t *testing.T) {
	if n := len(os.Args); n >= 2 && os.Args[n-2] == "image-limits-helper" {
		t.Setenv("XDG_CONFIG_HOME", os.Args[n-1])
		reset := time.Now().Add(8 * time.Hour).Round(time.Second)
		NoteCodexImageLimit("codex", "other@example.com", CodexLimit{Used: 0, Span: 24 * time.Hour, Resets: reset})
		NoteCodexImageLimit("codex", "me@example.com", CodexLimit{Used: 100, Span: 24 * time.Hour, Resets: reset})
		return
	}
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	now := time.Now()
	reset := now.Add(12 * time.Hour).Round(time.Second)
	for _, user := range []string{"me@example.com", "kept@example.com"} {
		NoteCodexImageLimit("codex", user, CodexLimit{Used: 0, Span: 24 * time.Hour, Resets: reset})
	}
	if _, ok := CodexImageLimit("codex", "other@example.com", now); ok {
		t.Fatal("an account that has not drawn is known")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := proc.CommandContext(ctx, binary, "-test.run=^TestCodexImageLimitsOtherProcess$", "--", "image-limits-helper", config)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("another process: %v\n%s", err, out)
	}
	// This writer still remembers its older me@ reading. Saving another
	// account must merge the file anew, not overwrite the child's update.
	NoteCodexImageLimit("codex", "added@example.com", CodexLimit{Span: 24 * time.Hour, Resets: reset})
	for _, user := range []string{"me@example.com", "kept@example.com", "other@example.com", "added@example.com"} {
		limit, ok := CodexImageLimit("codex", user, now)
		if !ok || (user == "me@example.com") != (limit.Used == 100) {
			t.Fatalf("after another process wrote %s: %+v, known %v", user, limit, ok)
		}
	}
}

func TestCodexImageLimitsUnknownSupersedesEarlierReading(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Now()
	NoteCodexImageLimit("codex", "me@example.com", CodexLimit{Span: 24 * time.Hour, Resets: now.Add(12 * time.Hour)})
	// As another process does: a later refusal contradicts the older
	// available headers. Keep the invalidation as well, so a reader that
	// had that older snapshot in memory does not bring it back.
	m, err := imageLimitsRead(imageLimitsPath(), true)
	if err != nil {
		t.Fatal(err)
	}
	out := imageLimits{}
	for k, v := range m {
		out[k] = v
	}
	out["codex/me@example.com"] = imageLimitObservation{At: time.Now().Add(time.Second)}
	b, _ := json.Marshal(out)
	if err := os.WriteFile(imageLimitsPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := CodexImageLimit("codex", "me@example.com", now); ok {
		t.Fatal("a later unknown observation revived an earlier available reading")
	}
	forgetImageLimitMemory()
	if _, ok := CodexImageLimit("codex", "me@example.com", now); ok {
		t.Fatal("a restart revived an available reading contradicted by a refusal")
	}
	NoteCodexImageLimit("codex", "me@example.com", CodexLimit{Used: 100, Span: 24 * time.Hour})
	forgetImageLimitMemory()
	if _, ok := CodexImageLimit("codex", "me@example.com", now); ok {
		t.Fatal("headers without a reset claimed an image allowance")
	}
}

func TestCodexImageLimitsBadFileIsUnknown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := imageLimitsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"null", `{"codex/me@example.com":`} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		forgetImageLimitMemory()
		if _, ok := CodexImageLimit("codex", "me@example.com", time.Now()); ok {
			t.Fatalf("a bad file (%s) reported an available image window", bad)
		}
	}
	// A new genuine reading can be kept, with the malformed input kept
	// aside rather than silently overwritten as an empty map.
	NoteCodexImageLimit("codex", "me@example.com", CodexLimit{Span: 24 * time.Hour, Resets: time.Now().Add(time.Hour)})
	if bad, _ := filepath.Glob(path + ".bad-*"); len(bad) != 1 {
		t.Fatalf("malformed state was not preserved: %v", bad)
	}
	forgetImageLimitMemory()
	if _, ok := CodexImageLimit("codex", "me@example.com", time.Now()); !ok {
		t.Fatal("a valid observation after malformed state was lost")
	}
}

// A directory at the state file's path is unreadable on every platform,
// even for root: it tests a real read/write failure without permissions
// that a privileged test runner could ignore.
func TestCodexImageLimitsUnreadableFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := imageLimitsPath()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	forgetImageLimitMemory()
	now := time.Now()
	if _, ok := CodexImageLimit("codex", "me@example.com", now); ok {
		t.Fatal("an unreadable file reported an image window before any reply")
	}
	reset := now.Add(12 * time.Hour).Round(time.Second)
	limit := CodexLimit{Span: 24 * time.Hour, Resets: reset}
	NoteCodexImageLimit("codex", "me@example.com", limit)
	if got, ok := CodexImageLimit("codex", "me@example.com", now); !ok || !got.Resets.Equal(reset) {
		t.Fatalf("a failed write lost the live observation: %+v, known %v", got, ok)
	}
	forgetImageLimitMemory()
	if _, ok := CodexImageLimit("codex", "me@example.com", now); ok {
		t.Fatal("a restart recovered a window that could not be written")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	NoteCodexImageLimit("codex", "me@example.com", limit)
	forgetImageLimitMemory()
	if got, ok := CodexImageLimit("codex", "me@example.com", now); !ok || !got.Resets.Equal(reset) {
		t.Fatalf("a new reply after storage recovered was not persisted: %+v, known %v", got, ok)
	}
}
