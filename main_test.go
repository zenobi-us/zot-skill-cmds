package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patriceckhart/zot/packages/agent/ext"
)

func writeSkill(t *testing.T, root, dir, content string) string {
	t.Helper()
	path := filepath.Join(root, dir, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func isolateUserHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
}

func TestParseSkillUserInvocableIsTriState(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		body string
		set  bool
		want bool
	}{
		{"undefined", "---\nname: developer:review\n---\nbody", false, false},
		{"true", "---\nname: developer:review\nuser-invocable: true\n---\nbody", true, true},
		{"false", "---\nname: developer:review\nuser-invocable: false\n---\nbody", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeSkill(t, root, tc.name, tc.body)
			s, err := parseSkill(path, root)
			if err != nil {
				t.Fatal(err)
			}
			if s.userSet != tc.set || s.userValue != tc.want {
				t.Fatalf("user-invocable = (%v, %v), want (%v, %v)", s.userSet, s.userValue, tc.set, tc.want)
			}
		})
	}
}

func TestAliasForNamespacedSkill(t *testing.T) {
	for input, want := range map[string]string{
		"developer:commit":      "commit",
		"review":                "review",
		"Developer:Make Commit": "make-commit",
	} {
		if got := aliasFor(input); got != want {
			t.Errorf("aliasFor(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExtensionSkillRootsReadsEnabledManifest(t *testing.T) {
	isolateUserHome(t)
	cwd := t.TempDir()
	state := filepath.Join(cwd, "state")
	t.Setenv("ZOT_HOME", state)
	extensionDir := filepath.Join(state, "extensions", "developer")
	if err := os.MkdirAll(filepath.Join(extensionDir, "skills", "commit"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extensionDir, "extension.json"), []byte(`{"name":"developer","skills":["./skills"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(extensionDir, "skills"), "commit", "---\nname: commit\nuser-invocable: true\n---\nbody")

	roots := extensionSkillRoots(cwd)
	if len(roots) != 1 || roots[0].path != filepath.Join(extensionDir, "skills") || roots[0].namespace != "developer" {
		t.Fatalf("extension skill roots = %v", roots)
	}
	found := discover(cwd)
	if len(found) != 1 || found[0].name != "developer:commit" || !found[0].userValue {
		t.Fatalf("discovered skills = %+v", found)
	}
}

func TestExtensionSkillRootsRejectsTraversal(t *testing.T) {
	cwd := t.TempDir()
	state := filepath.Join(cwd, "state")
	t.Setenv("ZOT_HOME", state)
	extensionDir := filepath.Join(state, "extensions", "bad")
	if err := os.MkdirAll(extensionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extensionDir, "extension.json"), []byte(`{"name":"bad","skills":["../outside"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if roots := extensionSkillRoots(cwd); len(roots) != 0 {
		t.Fatalf("traversal skill roots = %v", roots)
	}
}

func TestInvokeCallsBuiltinSkillTool(t *testing.T) {
	root := t.TempDir()
	path := writeSkill(t, root, "commit", "---\nname: developer:commit\ndescription: stale description\nuser-invocable: true\n---\nstale instructions")
	var calledName string
	var calledArgs any
	a := &app{
		callTool: func(_ context.Context, name string, args any) (ext.ToolResult, error) {
			calledName = name
			calledArgs = args
			return ext.TextResult("# Skill: developer:commit\n\nFresh instructions from zot."), nil
		},
		skill: map[string]skill{
			"commit": {name: "developer:commit", path: path},
		},
	}

	response := a.invoke("commit", "commit the current changes")
	if response.Action != "prompt" {
		t.Fatalf("response action = %q, want prompt", response.Action)
	}
	if calledName != "skill" {
		t.Fatalf("called tool = %q, want skill", calledName)
	}
	args, ok := calledArgs.(map[string]string)
	if !ok || args["name"] != "developer:commit" {
		t.Fatalf("called args = %#v", calledArgs)
	}
	want := "Use the following skill for this request. Follow its instructions." +
		"\n\nSkill directory: " + filepath.Dir(path) +
		"\n\n# Skill: developer:commit\n\nFresh instructions from zot." +
		"\n\n---\n\nUser request:\ncommit the current changes"
	if response.Prompt != want {
		t.Fatalf("prompt = %q, want %q", response.Prompt, want)
	}
	if strings.Contains(response.Prompt, "stale instructions") || strings.Contains(response.Prompt, "Use the skill tool to load") {
		t.Fatal("prompt did not use the instructions returned by zot")
	}
}

func TestInvokeReportsSkillToolFailures(t *testing.T) {
	base := app{skill: map[string]skill{"commit": {name: "developer:commit", path: "/skills/commit/SKILL.md"}}}
	tests := []struct {
		name     string
		callTool toolCaller
		want     string
	}{
		{
			name: "unsupported host",
			callTool: func(context.Context, string, any) (ext.ToolResult, error) {
				return ext.ToolResult{}, errors.New("host does not support call_tool")
			},
			want: "zot v0.3.95 or newer is required",
		},
		{
			name: "transport error",
			callTool: func(context.Context, string, any) (ext.ToolResult, error) {
				return ext.ToolResult{}, context.DeadlineExceeded
			},
			want: "context deadline exceeded",
		},
		{
			name: "tool error",
			callTool: func(context.Context, string, any) (ext.ToolResult, error) {
				return ext.ToolResult{IsError: true, Content: []ext.ToolContent{ext.Text(`skill: no skill named "developer:commit"`)}}, nil
			},
			want: "no skill named",
		},
		{
			name: "empty result",
			callTool: func(context.Context, string, any) (ext.ToolResult, error) {
				return ext.ToolResult{}, nil
			},
			want: "returned no text",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := base
			a.callTool = tc.callTool
			response := a.invoke("commit", "")
			if response.Error == "" || !strings.Contains(response.Error, tc.want) {
				t.Fatalf("response error = %q, want it to contain %q", response.Error, tc.want)
			}
		})
	}
}

func TestNamespaceCommandsUseExtensionName(t *testing.T) {
	isolateUserHome(t)
	cwd := t.TempDir()
	state := filepath.Join(cwd, "state")
	t.Setenv("ZOT_HOME", state)
	extensionDir := filepath.Join(state, "extensions", "developer")
	if err := os.MkdirAll(filepath.Join(extensionDir, "skills", "commit"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extensionDir, "extension.json"), []byte(`{"name":"developer","skills":["./skills"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(extensionDir, "skills"), "commit", "---\nname: commit\nuser-invocable: true\n---\nbody")

	found := discover(cwd)
	if len(found) != 1 || found[0].namespace != "developer" {
		t.Fatalf("discovered skills = %+v", found)
	}
	a := &app{config: config{NamespaceCommands: true}, skill: map[string]skill{}}
	for _, s := range found {
		alias := aliasFor(s.name)
		if a.config.NamespaceCommands && s.namespace != "" {
			alias = kebab(s.namespace) + "-" + alias
		}
		if alias != "developer-commit" {
			t.Fatalf("namespaced alias = %q", alias)
		}
	}
}

func TestConfigCommandWritesStateFile(t *testing.T) {
	t.Setenv("ZOT_HOME", t.TempDir())
	a := &app{}
	response := a.configCommand("namespace on")
	if response.Action != "display" {
		t.Fatalf("response action = %q", response.Action)
	}
	loaded, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.NamespaceCommands {
		t.Fatal("namespace setting was not persisted")
	}
	if filepath.Dir(configPath()) != os.Getenv("ZOT_HOME") {
		t.Fatalf("config path = %q", configPath())
	}
}

func TestDiscoverUsesPrecedenceAndOnlyExplicitTrue(t *testing.T) {
	isolateUserHome(t)
	cwd := t.TempDir()
	t.Setenv("ZOT_HOME", filepath.Join(cwd, "state"))
	writeSkill(t, cwd, filepath.Join(".claude", "skills", "developer", "commit"), "---\nname: developer:commit\ndescription: project\nuser-invocable: true\n---\nproject body")
	writeSkill(t, cwd, filepath.Join(".zot", "skills", "developer", "commit"), "---\nname: developer:commit\ndescription: higher\nuser-invocable: false\n---\nhigher body")
	writeSkill(t, cwd, filepath.Join(".claude", "skills", "review"), "---\nname: review\n---\nnot aliased")
	writeSkill(t, cwd, filepath.Join(".claude", "skills", "test"), "---\nname: developer:test\nuser-invocable: true\n---\ntest body")

	found := discover(cwd)
	aliases := map[string]bool{}
	for _, s := range found {
		if s.userSet && s.userValue {
			aliases[aliasFor(s.name)] = true
		}
	}
	if aliases["commit"] {
		t.Fatal("lower-precedence commit was aliased")
	}
	if aliases["test"] != true {
		t.Fatal("explicitly user-invocable test was not aliased")
	}
	if aliases["review"] {
		t.Fatal("undefined user-invocable skill was aliased")
	}
}
