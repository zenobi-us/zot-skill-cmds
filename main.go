package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/patriceckhart/zot/packages/agent/ext"
)

const extensionName = "zot-skill-cmds"

var version = "0.1.0"

type skill struct {
	name        string
	description string
	path        string
	body        string
	userSet     bool
	userValue   bool
}

type app struct {
	ext   *ext.Extension
	cwd   string
	skill map[string]skill
}

func logf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "[%s] %s\n", extensionName, fmt.Sprintf(format, args...))
}

func zotHome() string {
	if value := os.Getenv("ZOT_HOME"); value != "" {
		return absolute(value)
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "zot")
}

func absolute(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	cwd, _ := os.Getwd()
	return filepath.Clean(filepath.Join(cwd, path))
}

func skillRoots(cwd string) []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(cwd, ".zot", "skills"),
		filepath.Join(zotHome(), "skills"),
		filepath.Join(cwd, ".claude", "skills"),
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(cwd, ".agents", "skills"),
		filepath.Join(home, ".agents", "skills"),
	}
}

func kebab(value string) string {
	var b strings.Builder
	separator := false
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if separator && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			separator = false
		} else {
			separator = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func parseValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	return value
}

// parseSkill reads the small frontmatter subset needed by this extension.
// Unknown fields remain ignored, as they are by Claude Code and zot.
func parseSkill(path string, root string) (skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return skill{}, err
	}
	front, body, ok := splitFrontmatter(string(data))
	if !ok {
		return skill{}, errors.New("missing or malformed frontmatter")
	}
	result := skill{path: path, body: strings.TrimSpace(body)}
	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		colon := strings.IndexByte(trimmed, ':')
		if colon < 0 {
			continue
		}
		key, value := strings.TrimSpace(trimmed[:colon]), parseValue(trimmed[colon+1:])
		switch key {
		case "name":
			result.name = value
		case "description":
			result.description = value
		case "user-invocable", "user_invocable":
			result.userSet = true
			result.userValue = strings.EqualFold(value, "true")
		}
	}
	if result.name == "" {
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return skill{}, err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		for i := range parts {
			parts[i] = kebab(parts[i])
		}
		result.name = strings.Join(parts, "-")
	}
	return result, nil
}

func splitFrontmatter(data string) (front, body string, ok bool) {
	data = strings.TrimPrefix(data, "\ufeff")
	if !strings.HasPrefix(data, "---") {
		return "", data, false
	}
	newline := strings.IndexByte(data, '\n')
	if newline < 0 {
		return "", data, false
	}
	rest := data[newline+1:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", data, false
	}
	return rest[:end], strings.TrimLeft(rest[end+len("\n---"):], " \t\r\n"), true
}

func discover(cwd string) []skill {
	seen := map[string]bool{}
	var result []skill
	for _, root := range skillRoots(cwd) {
		root, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				logf("cannot scan %s: %v", path, walkErr)
				return nil
			}
			if entry.IsDir() || entry.Name() != "SKILL.md" {
				return nil
			}
			s, err := parseSkill(path, root)
			if err != nil {
				logf("ignoring %s: %v", path, err)
				return nil
			}
			if seen[s.name] {
				return nil
			}
			seen[s.name] = true
			result = append(result, s)
			return nil
		})
		if err != nil {
			logf("cannot scan %s: %v", root, err)
		}
	}
	return result
}

func aliasFor(name string) string {
	if index := strings.LastIndexByte(name, ':'); index >= 0 {
		name = name[index+1:]
	}
	return kebab(name)
}

func validCommand(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/: \t\r\n")
}

func (a *app) reload() {
	found := discover(a.cwd)
	aliases := map[string]skill{}
	for _, s := range found {
		// The extension intentionally treats omitted user-invocable as
		// undefined. Only an explicit true registers a bare command.
		if !s.userSet || !s.userValue {
			continue
		}
		alias := aliasFor(s.name)
		if !validCommand(alias) {
			logf("ignoring invalid alias %q for skill %q", alias, s.name)
			continue
		}
		if prior, exists := aliases[alias]; exists {
			logf("alias /%s from %s conflicts with skill %q; keeping %q", alias, s.path, prior.name, prior.name)
			continue
		}
		aliases[alias] = s
	}
	a.skill = aliases
	keys := make([]string, 0, len(aliases))
	for key := range aliases {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, alias := range keys {
		s := aliases[alias]
		a.ext.Command(alias, fmt.Sprintf("invoke skill %s", s.name), func(args string) ext.Response {
			return a.invoke(alias, args)
		})
	}
	logf("registered %d skill command(s)", len(keys))
}

func (a *app) invoke(alias, args string) ext.Response {
	// Read the file again so edits take effect without restarting zot.
	s, ok := a.skill[alias]
	if !ok {
		return ext.Errorf("skill alias /%s is no longer available", alias)
	}
	fresh, err := parseSkill(s.path, filepath.Dir(filepath.Dir(s.path)))
	if err != nil {
		return ext.Errorf("cannot load skill %q: %v", s.name, err)
	}
	if fresh.name == "" {
		fresh.name = s.name
	}
	prompt := fmt.Sprintf("Use the following skill for this request. Follow its instructions.\n\n# Skill: %s\n\n", fresh.name)
	if fresh.description != "" {
		prompt += fresh.description + "\n\n"
	}
	prompt += "Skill directory: " + filepath.Dir(fresh.path) + "\n\n---\n\n" + strings.TrimSpace(fresh.body)
	if args = strings.TrimSpace(args); args != "" {
		prompt += "\n\n---\n\nUser request:\n" + args
	}
	return ext.Prompt(prompt)
}

func main() {
	a := &app{ext: ext.New(extensionName, version), skill: map[string]skill{}}
	a.ext.OnHello(func(info ext.HostInfo) {
		a.cwd = info.CWD
		a.reload()
	})
	if err := a.ext.Run(); err != nil && !errors.Is(err, io.EOF) {
		logf("fatal: %v", err)
		return
	}
}
