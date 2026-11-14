package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// kennel mcp is a stdio MCP server with one MCP App: the harness tool opens
// ui://kennel/harness on one scope and harness, and the app reads everything
// else through the kennel tool, which runs one kennel command. So every
// action in the app is a kennel command an agent can run itself.
//
// The app acts only on what this server showed it: file log and file show
// read only files a harness show or vault ls listed; vault add, unlink and
// update take only a skill folder a harness show listed, vault copy writes
// only into a skills folder one listed, and vault install --project only into
// a scope one resolved.

const (
	appURI  = "ui://kennel/harness"
	appMIME = "text/html;profile=mcp-app"
)

//go:embed app/harness.html app/marked.umd.js app/purify.min.js
var appFiles embed.FS

// appCommands are the kennel commands the app may run, by their first words.
var appCommands = [][]string{
	{"harness", "show"},
	{"harness", "recent"},
	{"file", "log"},
	{"file", "show"},
	{"vault", "ls"},
	{"vault", "add"},
	{"vault", "install"},
	{"vault", "unlink"},
	{"vault", "copy"},
	{"vault", "update"},
	{"vault", "rm"},
}

// appHTML is the app page with marked and DOMPurify inlined: the host's
// frame loads no scripts from the network.
func appHTML() (string, error) {
	page, err := appFiles.ReadFile("app/harness.html")
	if err != nil {
		return "", err
	}
	html := string(page)
	for marker, file := range map[string]string{"/*MARKED*/": "app/marked.umd.js", "/*PURIFY*/": "app/purify.min.js"} {
		js, err := appFiles.ReadFile(file)
		if err != nil {
			return "", err
		}
		html = strings.Replace(html, marker, string(js), 1)
	}
	return html, nil
}

// shown is every file, skill folder, skills folder and scope a harness show
// or vault ls run by this server listed.
type shown struct {
	mu        sync.Mutex
	files     map[string]bool
	skills    map[string]bool // skill folders on a Skills page
	skillDirs map[string]bool // the folders a harness loads skills from
	scopes    map[string]bool // project directories a show resolved
	items     map[string]bool // vault items a vault ls listed
}

func newShown() *shown {
	return &shown{files: map[string]bool{}, skills: map[string]bool{}, skillDirs: map[string]bool{}, scopes: map[string]bool{}, items: map[string]bool{}}
}

// add records what one harness show --json or vault ls --json output lists.
func (s *shown) add(out string) {
	var show struct {
		Section string `json:"section"`
		Scope   struct {
			Dir string `json:"dir"`
		} `json:"scope"`
		Skills []struct {
			Item  string   `json:"item"`
			Path  string   `json:"path"`
			Files []string `json:"files"`
		} `json:"skills"`
		Snippets []struct {
			Item string `json:"item"`
			Path string `json:"path"`
		} `json:"snippets"`
		Blocks []struct {
			Dirs    []string `json:"dirs"`
			Entries []struct {
				Path   string   `json:"path"`
				Files  []string `json:"files"`
				File   string   `json:"file"`
				Script string   `json:"script"`
			} `json:"entries"`
		} `json:"blocks"`
	}
	if json.Unmarshal([]byte(out), &show) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if show.Scope.Dir != "" {
		s.scopes[filepath.Clean(show.Scope.Dir)] = true
	}
	for _, it := range show.Skills {
		s.items[it.Item] = true
		for _, f := range it.Files {
			s.files[filepath.Join(it.Path, f)] = true
		}
	}
	for _, it := range show.Snippets {
		s.items[it.Item] = true
		s.files[filepath.Clean(it.Path)] = true
	}
	for _, b := range show.Blocks {
		for _, d := range b.Dirs {
			s.skillDirs[filepath.Clean(d)] = true
		}
		for _, e := range b.Entries {
			if show.Section == "skills" && e.Path != "" {
				s.skills[filepath.Clean(e.Path)] = true
			}
			for _, f := range e.Files {
				s.files[filepath.Join(e.Path, f)] = true
			}
			if e.Files == nil && e.Path != "" {
				s.files[filepath.Clean(e.Path)] = true
			}
			for _, f := range []string{e.File, e.Script} {
				if f != "" {
					s.files[filepath.Clean(f)] = true
				}
			}
		}
	}
}

func (s *shown) has(set map[string]bool, path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return set[filepath.Clean(path)]
}

func (s *shown) hasItem(item string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[item]
}

func newMCPServer() *mcp.Server {
	seen := newShown()
	destructive := true
	s := mcp.NewServer(&mcp.Implementation{Name: "kennel", Version: "0.1.0"}, nil)

	s.AddResource(&mcp.Resource{URI: appURI, Name: "harness", Title: "kennel harness", MIMEType: appMIME},
		func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			html, err := appHTML()
			if err != nil {
				return nil, err
			}
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: appURI, MIMEType: appMIME, Text: html}}}, nil
		})

	s.AddTool(&mcp.Tool{
		Name:        "harness",
		Title:       "kennel harness",
		Description: "Open kennel's harness app: the skills, guidance, permissions, hooks and MCP servers Claude, Codex or pi gets in one directory, or globally when no directory is given.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"directory":{"type":"string","description":"A project directory; omit for the global scope"},` +
			`"harness":{"type":"string","enum":["claude","codex","pi"],"description":"Defaults to claude"}}}`),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Meta:        mcp.Meta{"ui": map[string]any{"resourceUri": appURI}, "ui/resourceUri": appURI},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return harnessTool(ctx, req, seen)
	})

	s.AddTool(&mcp.Tool{
		Name:        "kennel",
		Description: "Run one kennel command for the harness app: harness show, harness recent, file log, file show, or vault ls, add, install, unlink, copy, update or rm. vault add moves the original folder to the Trash; vault rm moves the vault item there.",
		InputSchema: json.RawMessage(`{"type":"object","required":["args"],"properties":{` +
			`"args":{"type":"array","items":{"type":"string"},"description":"The command's words after kennel"}}}`),
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive},
		Meta:        mcp.Meta{"ui": map[string]any{"resourceUri": appURI, "visibility": []string{"app"}}},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return kennelTool(ctx, req, seen)
	})
	return s
}

// harnessTool resolves the scope with kennel harness show and hands the app
// its skills page: a missing directory is Global, a missing harness Claude.
func harnessTool(ctx context.Context, req *mcp.CallToolRequest, seen *shown) (*mcp.CallToolResult, error) {
	var in struct {
		Directory string `json:"directory"`
		Harness   string `json:"harness"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return toolError(err.Error()), nil
		}
	}
	scope := strings.TrimSpace(in.Directory)
	if scope == "" {
		scope = "global"
	}
	if in.Harness == "" {
		in.Harness = "claude"
	}
	out, err := runKennel(ctx, "harness", "show", "skills", "--scope", scope, "--harness", in.Harness, "--json")
	if err != nil {
		return toolError(err.Error()), nil
	}
	seen.add(out)
	var show struct {
		Harness string `json:"harness"`
		Scope   struct {
			Kind    string `json:"kind"`
			Root    string `json:"root"`
			Profile string `json:"profile"`
			Home    string `json:"home"`
		} `json:"scope"`
	}
	if err := json.Unmarshal([]byte(out), &show); err != nil {
		return toolError(err.Error()), nil
	}
	text := fmt.Sprintf("Opened kennel harness: global %s, %s.", show.Harness, tilde(show.Scope.Home))
	if show.Scope.Kind == "project" {
		profile := show.Scope.Profile
		if profile == "" {
			profile = "default"
		}
		text = fmt.Sprintf("Opened kennel harness: %s, %s, profile %s.", tilde(show.Scope.Root), show.Harness, profile)
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: json.RawMessage(out),
	}, nil
}

// kennelTool runs one command from appCommands and returns what it printed;
// file log and file show take only one file a harness show listed.
func kennelTool(ctx context.Context, req *mcp.CallToolRequest, seen *shown) (*mcp.CallToolResult, error) {
	var in struct {
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
		return toolError(err.Error()), nil
	}
	allowed := slices.ContainsFunc(appCommands, func(c []string) bool {
		return len(in.Args) >= len(c) && slices.Equal(in.Args[:len(c)], c)
	})
	if !allowed {
		return toolError(fmt.Sprintf("kennel %s is not a command the app runs", strings.Join(in.Args, " "))), nil
	}
	if in.Args[0] == "file" {
		pos := slices.DeleteFunc(slices.Clone(in.Args[2:]), func(a string) bool { return a == "--json" })
		if len(pos) != 1 {
			return toolError(fileUsage), nil
		}
		if path, _ := splitRev(pos[0]); !seen.has(seen.files, path) {
			return toolError(fmt.Sprintf("%s is not a file the app has listed", tilde(path))), nil
		}
	}
	if in.Args[0] == "harness" && !harnessArgsOK(in.Args[1:]) {
		return toolError(fmt.Sprintf("kennel %s is not a command the app runs", strings.Join(in.Args, " "))), nil
	}
	if in.Args[0] == "vault" {
		if msg := vaultGate(in.Args[1:], seen); msg != "" {
			return toolError(msg), nil
		}
	}
	out, err := runKennel(ctx, in.Args...)
	if err != nil {
		return toolError(err.Error()), nil
	}
	if (in.Args[0] == "harness" && in.Args[1] == "show") || (in.Args[0] == "vault" && in.Args[1] == "ls") {
		seen.add(out)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}}, nil
}

// harnessArgsOK reports whether a harness command has the words the app sends:
// recent [--json], or show <section> with --scope <dir>, --harness <name> and
// --json, each at most once.
func harnessArgsOK(args []string) bool {
	if args[0] == "recent" {
		return len(args) == 1 || (len(args) == 2 && args[1] == "--json")
	}
	if len(args) < 2 || !slices.Contains(harnessSections, args[1]) {
		return false
	}
	used := map[string]bool{}
	for rest := args[2:]; len(rest) > 0; rest = rest[1:] {
		flag := rest[0]
		if used[flag] {
			return false
		}
		used[flag] = true
		switch flag {
		case "--json":
		case "--scope", "--harness":
			if len(rest) < 2 {
				return false
			}
			rest = rest[1:]
		default:
			return false
		}
	}
	return true
}

// vaultGate checks one vault command's words against what the app was shown,
// returning why it is refused, or "".
func vaultGate(args []string, seen *shown) string {
	n := len(args)
	if (args[0] == "rm" || args[0] == "copy" || args[0] == "install") && (n < 2 || !seen.hasItem(args[1])) {
		return fmt.Sprintf("%s is not a vault item the app has listed", strings.Join(args[1:min(2, n)], ""))
	}
	switch args[0] {
	case "ls":
		if n == 1 || (n == 2 && args[1] == "--json") {
			return ""
		}
	case "rm":
		if n == 2 {
			return ""
		}
	case "add", "unlink", "update":
		if n == 2 {
			if !seen.has(seen.skills, args[1]) {
				return fmt.Sprintf("%s is not a skill the app has listed", tilde(args[1]))
			}
			return ""
		}
	case "copy":
		if n == 3 {
			// a new folder named directly inside a listed skills folder
			dst := args[2]
			if filepath.Clean(dst) != dst || !seen.has(seen.skillDirs, filepath.Dir(dst)) {
				return fmt.Sprintf("%s is not in a skills folder the app has listed", tilde(dst))
			}
			if _, err := os.Lstat(dst); err == nil {
				return fmt.Sprintf("%s already exists", tilde(dst))
			}
			return ""
		}
	case "install":
		if n == 3 {
			return ""
		}
		if n == 5 && args[3] == "--project" {
			if !seen.has(seen.scopes, args[4]) {
				return fmt.Sprintf("%s is not a project the app has opened", tilde(args[4]))
			}
			return ""
		}
	}
	return fmt.Sprintf("kennel vault %s: the app does not run it with these arguments", strings.Join(args, " "))
}

// runKennel runs this kennel binary with args, so the app and the CLI share
// one code path; a failure carries what the command printed to stderr.
func runKennel(ctx context.Context, args ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	c := exec.CommandContext(ctx, exe, args...)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(strings.TrimPrefix(errb.String(), "kennel: "))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return out.String(), nil
}

func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

func cmdMCP(args []string) int {
	if len(args) != 0 {
		return failf("usage: kennel mcp")
	}
	if err := newMCPServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		return failf("%v", err)
	}
	return 0
}
