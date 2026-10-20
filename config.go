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
}

type Tool struct {
	Name      string
	Env       string
	EnvSource string // "built-in" or "config"
	Base      string
	Shared    []string
}

type Config struct {
	Path     string
	Tools    map[string]*Tool
	Profiles map[string]map[string]string // profile → tool → home
}

type rawTool struct {
	Env    string   `toml:"env"`
	Base   string   `toml:"base"`
	Shared []string `toml:"shared"`
}

type rawConfig struct {
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
	if d := os.Getenv("AGENTHOME_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(home(), ".config", "agenthome")
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
		return nil, fmt.Errorf("no config at %s (see `agenthome help config`)", path)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("%s: unknown key %s", path, und[0])
	}

	cfg := &Config{Path: path, Tools: map[string]*Tool{}, Profiles: map[string]map[string]string{}}
	var problems []string
	for name, rt := range raw.Tools {
		t := &Tool{Name: name, Env: rt.Env, EnvSource: "config", Base: rt.Base, Shared: rt.Shared}
		if t.Env == "" {
			t.Env, t.EnvSource = builtinEnv[name], "built-in"
		}
		if t.Env == "" {
			problems = append(problems, fmt.Sprintf("[tools.%s] needs `env`: agenthome has no built-in variable for %q", name, name))
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
