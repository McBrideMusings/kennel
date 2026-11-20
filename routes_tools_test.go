package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A route naming tools moves only those tools, and when it expires they fall
// back to the row under it; a later row that covers an older one for its
// whole life removes it.
func TestUseRoutesOneTool(t *testing.T) {
	h, cfg, err := shareSandbox(t, "[tools.codex]\nbase = \"~/.codex\"\nshared = []\n[profiles.personal]\n[profiles.work]\nclaude = \"~/.claude-work\"\ncodex = \"~/.codex-work\"\n")
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(h, "Work")
	os.MkdirAll(work, 0o755)
	os.WriteFile(routesPath(), []byte("~/Work work\n"), 0o644)
	t.Chdir(work)
	env := func() string { out, _ := quiet(t, func() int { return cmdEnv(cfg, false) }); return out }
	workEnv := "export CLAUDE_CONFIG_DIR=" + shellQuote(filepath.Join(h, ".claude-work")) + "\nexport CODEX_HOME=" + shellQuote(filepath.Join(h, ".codex-work")) + "\n"

	if _, code := quiet(t, func() int { return cmdUse(cfg, []string{"personal", "claude", "3h", "30m"}) }); code != 0 {
		t.Fatalf("use personal claude 3h 30m exited %d", code)
	}
	if got, want := env(), "unset CLAUDE_CONFIG_DIR\nexport CODEX_HOME="+shellQuote(filepath.Join(h, ".codex-work"))+"\n"; got != want {
		t.Errorf("env after use personal claude:\n%s\nwant:\n%s", got, want)
	}
	lines, _ := readRoutes()
	last := lines[len(lines)-1]
	exp, _ := expiryTime(last.expires)
	if last.format() != "~/Work personal:claude "+last.expires || time.Until(exp).Round(time.Minute) != 210*time.Minute {
		t.Errorf("new row %q, %v from now; want personal:claude for 3h30m", last.format(), time.Until(exp))
	}
	if got := routedProfiles(cfg, resolve(cfg, lines, work)); got != "claude=personal codex=work" {
		t.Errorf("which profile line = %q", got)
	}

	// The override expires: claude falls back to the permanent work row.
	lines[len(lines)-1].expires = "2000-01-01T00:00:00Z"
	writeRoutes(lines)
	if got := env(); got != workEnv {
		t.Errorf("env after the override expired:\n%s\nwant:\n%s", got, workEnv)
	}

	// use work for 1d covers the 2h personal row but not the permanent one.
	quiet(t, func() int { return cmdUse(cfg, []string{"personal", "claude", "2h"}) })
	quiet(t, func() int { return cmdUse(cfg, []string{"work"}) })
	lines, _ = readRoutes()
	var rows []string
	for _, l := range lines {
		if l.isRoute() {
			rows = append(rows, strings.Fields(l.format())[1])
		}
	}
	if got := strings.Join(rows, " "); got != "work work" {
		t.Errorf("rows after use work: %s, want the permanent work row and the new one", got)
	}
	exp, _ = expiryTime(lines[len(lines)-1].expires)
	if d := time.Until(exp); d < 23*time.Hour || d > 25*time.Hour {
		t.Errorf("default expiry is %v from now, want 1d", d)
	}
	if _, code := quiet(t, func() int { return cmdUse(cfg, []string{"work", "eow"}) }); code == 0 {
		t.Error("use work eow succeeded, want a refusal")
	}
}

// Rows with tools and a timestamp read back exactly as written, and a row
// whose only tool is unknown routes nothing rather than every tool.
func TestRouteLineTools(t *testing.T) {
	for _, raw := range []string{"~/my docs work:codex,pi 2026-11-21T19:30:00Z", "~/a work 2026-11-21", "~/b personal:claude"} {
		if l := parseRouteLine(raw, 1); l.bad != "" || l.format() != raw {
			t.Errorf("parseRouteLine(%q) = %q, bad %q", raw, l.format(), l.bad)
		}
	}
	if l := parseRouteLine("~/a work:", 1); l.bad == "" {
		t.Error("a row with an empty tool list read without complaint")
	}
	l := parseRouteLine("~/a work 2026-11-21", 1)
	if !l.expired(time.Date(2026, 11, 22, 0, 0, 0, 0, time.Local)) || l.expired(time.Date(2026, 11, 21, 23, 59, 0, 0, time.Local)) {
		t.Error("a last-day row must last through that day and no further")
	}
	cfg := &Config{Profiles: map[string]map[string]string{"work": {}}, Tools: map[string]*Tool{"claude": {}, "codex": {}}}
	if got := resolveMatch(cfg, []routeLine{parseRouteLine("/ work:cladue", 1)}, "/x", "claude", "codex"); len(got) != 0 {
		t.Errorf("a row naming only an unknown tool routed %v", got)
	}
}
