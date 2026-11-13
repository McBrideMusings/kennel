package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// kennel harness recent lists the directories Claude sessions ran in, newest
// first: the cwd of the newest session file in each folder of every profile's
// Claude projects folder. Worktrees, temp folders and folders that no longer
// exist are dropped. The app's project picker lists these.

const recentLimit = 50

type recentDir struct {
	Dir   string    `json:"dir"`
	Short string    `json:"short"` // Dir with ~ for the home folder
	Name  string    `json:"name"`
	Time  time.Time `json:"time"`
}

// projectsFolders lists every profile's Claude projects folder once, the
// base home's first; profiles often link theirs to the base home's.
func projectsFolders(cfg *Config) []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		h = filepath.Join(h, "projects")
		real, err := filepath.EvalSymlinks(h)
		if err != nil || seen[real] {
			return
		}
		seen[real] = true
		out = append(out, h)
	}
	add(toolBase(cfg, "claude"))
	names := make([]string, 0, len(cfg.Profiles))
	for n := range cfg.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if h := cfg.Profiles[n]["claude"]; h != "" {
			add(h)
		}
	}
	return out
}

// recentDirs stats every projects folder's session files, then reads cwds
// from the newest sessions down until it has recentLimit folders: a projects
// folder holds thousands of sessions, and reading each one costs seconds.
func recentDirs(cfg *Config) ([]recentDir, error) {
	type session struct {
		file string
		mod  time.Time
	}
	var sessions []session
	for _, p := range projectsFolders(cfg) {
		folders, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		for _, f := range folders {
			if !f.IsDir() || skipFolderName(f.Name()) {
				continue
			}
			if file, mod := newestSession(filepath.Join(p, f.Name())); file != "" {
				sessions = append(sessions, session{file, mod})
			}
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].mod.After(sessions[j].mod) })
	seen := map[string]bool{}
	out := []recentDir{}
	for _, s := range sessions {
		if len(out) == recentLimit {
			break
		}
		dir := sessionCwd(s.file)
		if dir == "" || seen[dir] || skipRecent(dir) || !isDir(dir) {
			continue
		}
		seen[dir] = true
		out = append(out, recentDir{Dir: dir, Short: tilde(dir), Name: filepath.Base(dir), Time: s.mod})
	}
	return out, nil
}

// newestSession is the most recently written .jsonl file in one projects folder.
func newestSession(folder string) (string, time.Time) {
	ents, err := os.ReadDir(folder)
	if err != nil {
		return "", time.Time{}
	}
	var best string
	var bestT time.Time
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err == nil && fi.ModTime().After(bestT) {
			best, bestT = filepath.Join(folder, e.Name()), fi.ModTime()
		}
	}
	return best, bestT
}

// sessionCwd is the cwd of the first line in a session file that names one.
func sessionCwd(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 0; sc.Scan() && n < 50; n++ {
		var l struct {
			Cwd string `json:"cwd"`
		}
		if !bytes.Contains(sc.Bytes(), []byte(`"cwd"`)) {
			continue
		}
		if json.Unmarshal(sc.Bytes(), &l) == nil && l.Cwd != "" {
			return l.Cwd
		}
	}
	return ""
}

// skipFolderName drops a projects folder whose name, Claude's encoding of
// the session's cwd with every / and . as -, is a temp folder or a worktree,
// before any of its files are read; skipRecent still checks the cwd itself.
func skipFolderName(name string) bool {
	return strings.HasPrefix(name, "-private-") || strings.HasPrefix(name, "-tmp-") ||
		strings.Contains(name, "--worktrees-") || strings.Contains(name, "-claude-worktrees-")
}

// skipRecent drops worktrees, temp folders and apps' folders under
// ~/Library from the picker.
func skipRecent(dir string) bool {
	tmp := filepath.Clean(os.TempDir())
	realTmp, _ := filepath.EvalSymlinks(tmp)
	for _, p := range []string{"/tmp", "/private/tmp", tmp, realTmp} {
		if p != "" && strings.HasPrefix(dir+"/", p+"/") {
			return true
		}
	}
	if strings.HasPrefix(dir+"/", filepath.Join(home(), "Library")+"/") { // apps' scratch folders
		return true
	}
	return strings.Contains(dir+"/", "/.worktrees/") || strings.Contains(dir+"/", "/.claude/worktrees/")
}

func cmdHarnessRecent(cfg *Config, args []string) int {
	asJSON := len(args) == 1 && args[0] == "--json"
	if len(args) > 1 || (len(args) == 1 && !asJSON) {
		return failf(harnessUsage)
	}
	dirs, err := recentDirs(cfg)
	if err != nil {
		return failf("%v", err)
	}
	if asJSON {
		return printJSON(dirs)
	}
	for _, d := range dirs {
		fmt.Println(tilde(d.Dir))
	}
	return 0
}
