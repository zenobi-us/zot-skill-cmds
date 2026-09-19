package main

import (
	"encoding/json"
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
	namespace   string
	userSet     bool
	userValue   bool
}

type skillRoot struct {
	path      string
	namespace string
}

type config struct {
	NamespaceCommands bool `json:"namespace_cmds,omitempty"`
}

type app struct {
	ext    *ext.Extension
	cwd    string
	config config
	skill  map[string]skill
}

const (
	configFileName    = "skill-cmds.json"
	configCommandName = "skill-cmds"
)

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

type extensionManifest struct {
	Name    string   `json:"name"`
	Enabled *bool    `json:"enabled,omitempty"`
	Skills  []string `json:"skills,omitempty"`
}

func (m extensionManifest) isEnabled() bool {
	return m.Enabled == nil || *m.Enabled
}

func extensionSkillRoots(cwd string) []skillRoot {
	roots := []skillRoot{}
	extensionRoots := []string{filepath.Join(cwd, ".zot", "extensions"), filepath.Join(zotHome(), "extensions")}
	for _, extensionsRoot := range extensionRoots {
		entries, err := os.ReadDir(extensionsRoot)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			dir := filepath.Join(extensionsRoot, entry.Name())
			info, err := os.Stat(dir)
			if err != nil || !info.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, "extension.json"))
			if err != nil {
				continue
			}
			var manifest extensionManifest
			if json.Unmarshal(data, &manifest) != nil || !manifest.isEnabled() {
				continue
			}
			for _, manifestSkillRoot := range manifest.Skills {
				root := filepath.Clean(filepath.Join(dir, manifestSkillRoot))
				rel, err := filepath.Rel(dir, root)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					logf("ignoring skill root %q in extension %q: path escapes extension", manifestSkillRoot, manifest.Name)
					continue
				}
				roots = append(roots, skillRoot{path: root, namespace: manifest.Name})
			}
		}
	}
	return roots
}

func skillRoots(cwd string) []skillRoot {
	home, _ := os.UserHomeDir()
	roots := extensionSkillRoots(cwd)
	return append(roots,
		skillRoot{path: filepath.Join(cwd, ".zot", "skills")},
		skillRoot{path: filepath.Join(zotHome(), "skills")},
		skillRoot{path: filepath.Join(cwd, ".claude", "skills")},
		skillRoot{path: filepath.Join(home, ".claude", "skills")},
		skillRoot{path: filepath.Join(cwd, ".agents", "skills")},
		skillRoot{path: filepath.Join(home, ".agents", "skills")},
	)
}

func configPath() string {
	return filepath.Join(zotHome(), configFileName)
}

func loadConfig() (config, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return config{}, nil
	}
	if err != nil {
		return config{}, err
	}
	var result config
	if err := json.Unmarshal(data, &result); err != nil {
		return config{}, err
	}
	return result, nil
}

func saveConfig(value config) error {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
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
	for _, rootSpec := range skillRoots(cwd) {
		root, err := filepath.EvalSymlinks(rootSpec.path)
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
			s.namespace = rootSpec.namespace
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
		if a.config.NamespaceCommands && s.namespace != "" {
			alias = kebab(s.namespace) + "-" + alias
		}
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

func (a *app) configCommand(args string) ext.Response {
	args = strings.TrimSpace(args)
	if args == "" || strings.EqualFold(args, "show") {
		data, err := json.MarshalIndent(a.config, "", "  ")
		if err != nil {
			return ext.Errorf("cannot format configuration: %v", err)
		}
		return ext.Display(fmt.Sprintf("%s\n%s", configPath(), data))
	}
	fields := strings.Fields(args)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "namespace") || (strings.ToLower(fields[1]) != "on" && strings.ToLower(fields[1]) != "off") {
		return ext.Errorf("usage: /%s [show|namespace on|namespace off]", configCommandName)
	}
	a.config.NamespaceCommands = strings.EqualFold(fields[1], "on")
	if err := saveConfig(a.config); err != nil {
		return ext.Errorf("cannot save configuration: %v", err)
	}
	return ext.Display(fmt.Sprintf("saved %s; restart zot to apply namespace command changes", configPath()))
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
	skillDir := filepath.Dir(s.path)
	prompt := fmt.Sprintf("Use the skill tool to load the skill named %q, then follow its instructions for this request.", fresh.name)
	prompt += fmt.Sprintf("\n\nSkill path context:\n- SKILL.md: %s\n- Skill directory: %s\n- Resolve relative asset, reference, and script paths from the skill directory above, not from the user's project cwd.\n- The user's project cwd is still the working directory for project changes; use an absolute skill path (or cd to the skill directory) when reading or running bundled skill files.", s.path, skillDir)
	if args = strings.TrimSpace(args); args != "" {
		prompt += "\n\nUser request:\n" + args
	}
	return ext.Prompt(prompt)
}

func main() {
	a := &app{ext: ext.New(extensionName, version), skill: map[string]skill{}}
	a.ext.Command(configCommandName, "configure skill commands", a.configCommand)
	a.ext.OnHello(func(info ext.HostInfo) {
		a.cwd = info.CWD
		loaded, err := loadConfig()
		if err != nil {
			logf("using default configuration: %v", err)
		} else {
			a.config = loaded
		}
		a.reload()
	})
	if err := a.ext.Run(); err != nil && !errors.Is(err, io.EOF) {
		logf("fatal: %v", err)
		return
	}
}
