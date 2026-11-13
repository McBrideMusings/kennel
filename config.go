package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// builtinEnv names the variable each known program reads its home from. The
// vendors pick these names, so they ship in the binary; a tool's `env` key in
// the config replaces one if a vendor renames it.
var builtinEnv = map[string]string{
	"claude": "CLAUDE_CONFIG_DIR",
	"codex":  "CODEX_HOME",
	"pi":     "PI_CODING_AGENT_DIR",
}

type Tool struct {
	Name      string
	Env       string
	EnvSource string // "built-in" or "config"
	Base      string
	Shared    []string
}

// Share links one tool's files into another tool's home. A file share has
// Links; a folder share has Into and Items.
type Share struct {
	Name   string
	Source string
	Links  []string
	Into   string
	Items  []string
}

// HooksOptIn says which tools get Claude's portable and adapter hooks.
// Both off writes nothing.
type HooksOptIn struct {
	Codex bool `toml:"codex"`
	Pi    bool `toml:"pi"`
}

type Config struct {
	Path     string
	Hooks    HooksOptIn
	Shares   map[string]*Share
	Tools    map[string]*Tool
	Profiles map[string]map[string]string // profile → tool → home
}

type rawTool struct {
	Env    string   `toml:"env"`
	Base   string   `toml:"base"`
	Shared []string `toml:"shared"`
}

type rawShare struct {
	Source string   `toml:"source"`
	Links  []string `toml:"links"`
	Into   string   `toml:"into"`
	Items  []string `toml:"items"`
}

type rawConfig struct {
	Hooks    HooksOptIn                   `toml:"hooks"`
	Shares   map[string]rawShare          `toml:"shares"`
	Tools    map[string]rawTool           `toml:"tools"`
	Profiles map[string]map[string]string `toml:"profiles"`
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		fatalf("cannot find the home directory: %v", err)
	}
	return h
}

func configDir() string {
	if d := os.Getenv("KENNEL_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(home(), ".config", "kennel")
}

func configPath() string { return filepath.Join(configDir(), "config.toml") }
func routesPath() string { return filepath.Join(configDir(), "routes") }

// expand turns a leading ~ into the home directory and cleans the result.
func expand(p string) string {
	if p == "~" {
		return home()
	}
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(home(), p[2:])
	}
	return filepath.Clean(p)
}

// tilde is expand's inverse, for paths written back to a file or shown.
func tilde(p string) string {
	h := home()
	if p == h {
		return "~"
	}
	if strings.HasPrefix(p, h+"/") {
		return "~" + p[len(h):]
	}
	return p
}

func loadConfig() (*Config, error) {
	path := configPath()
	var raw rawConfig
	md, err := toml.DecodeFile(path, &raw)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no config at %s (see `kennel help config`)", path)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("%s: unknown key %s", path, und[0])
	}

	cfg := &Config{Path: path, Hooks: raw.Hooks, Tools: map[string]*Tool{}, Profiles: map[string]map[string]string{}, Shares: map[string]*Share{}}
	var problems []string
	for name, rt := range raw.Tools {
		t := &Tool{Name: name, Env: rt.Env, EnvSource: "config", Base: rt.Base, Shared: rt.Shared}
		if t.Env == "" {
			t.Env, t.EnvSource = builtinEnv[name], "built-in"
		}
		if t.Env == "" {
			problems = append(problems, fmt.Sprintf("[tools.%s] needs `env`: kennel has no built-in variable for %q", name, name))
		}
		if !md.IsDefined("tools", name, "base") {
			problems = append(problems, fmt.Sprintf("[tools.%s] needs `base`", name))
		}
		if !md.IsDefined("tools", name, "shared") {
			problems = append(problems, fmt.Sprintf("[tools.%s] needs `shared` (write `shared = []` to share nothing)", name))
		}
		t.Base = expand(t.Base)
		for _, item := range t.Shared {
			if item == "" || strings.Contains(item, "/") || item == "." || item == ".." {
				problems = append(problems, fmt.Sprintf("[tools.%s] shared item %q must be one name inside the base home", name, item))
			}
		}
		cfg.Tools[name] = t
	}
	for prof, homes := range raw.Profiles {
		if prof == "" || strings.ContainsAny(prof, " \t") {
			problems = append(problems, fmt.Sprintf("[profiles.%q] needs a name without spaces; routes rows are read by whitespace", prof))
		}
		cfg.Profiles[prof] = map[string]string{}
		for tool, h := range homes {
			t, ok := cfg.Tools[tool]
			if !ok {
				problems = append(problems, fmt.Sprintf("[profiles.%s] names tool %q, which has no [tools.%s] block", prof, tool, tool))
				continue
			}
			h = expand(h)
			if h == t.Base {
				problems = append(problems, fmt.Sprintf("[profiles.%s] %s home is the base home; leave %s out of the profile to use the base", prof, tool, tool))
			}
			cfg.Profiles[prof][tool] = h
		}
	}
	for name, rs := range raw.Shares {
		isFile := md.IsDefined("shares", name, "links")
		isFolder := md.IsDefined("shares", name, "into") || md.IsDefined("shares", name, "items")
		if rs.Source == "" {
			problems = append(problems, fmt.Sprintf("[shares.%s] needs `source`", name))
		}
		switch {
		case isFile && isFolder:
			problems = append(problems, fmt.Sprintf("[shares.%s] has both `links` and `into`/`items`; use one kind of share", name))
		case !isFile && !isFolder:
			problems = append(problems, fmt.Sprintf("[shares.%s] needs `links` (one file) or `into` and `items` (folders)", name))
		case isFolder && (rs.Into == "" || len(rs.Items) == 0):
			problems = append(problems, fmt.Sprintf("[shares.%s] a folder share needs both `into` and `items`", name))
		case isFile && len(rs.Links) == 0:
			problems = append(problems, fmt.Sprintf("[shares.%s] `links` is empty", name))
		}
		s := &Share{Name: name, Source: expand(rs.Source), Into: rs.Into, Items: rs.Items}
		if s.Into != "" {
			s.Into = expand(s.Into)
		}
		for _, l := range rs.Links {
			s.Links = append(s.Links, expand(l))
		}
		for _, item := range rs.Items {
			if item == "" || strings.Contains(item, "/") || item == "." || item == ".." {
				problems = append(problems, fmt.Sprintf("[shares.%s] item %q must be one name with no /", name, item))
			}
		}
		cfg.Shares[name] = s
	}
	if _, ok := cfg.Tools["claude"]; !ok && (cfg.Hooks.Codex || cfg.Hooks.Pi) {
		problems = append(problems, "[hooks] needs a [tools.claude] block: Claude's settings.json and hooks/ live in its base")
	}
	if _, ok := cfg.Tools["codex"]; !ok && cfg.Hooks.Codex {
		problems = append(problems, "[hooks] codex = true needs a [tools.codex] block: hooks.json lives in its base")
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("%s:\n  %s", path, strings.Join(problems, "\n  "))
	}
	return cfg, nil
}

func (c *Config) toolNames() []string {
	names := make([]string, 0, len(c.Tools))
	for n := range c.Tools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (c *Config) profileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (c *Config) shareNames() []string {
	names := make([]string, 0, len(c.Shares))
	for n := range c.Shares {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
