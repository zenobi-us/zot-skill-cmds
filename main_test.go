package main

import (
	"os"
	"path/filepath"
	"testing"
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
	if len(roots) != 1 || roots[0] != filepath.Join(extensionDir, "skills") {
		t.Fatalf("extension skill roots = %v", roots)
	}
	found := discover(cwd)
	if len(found) != 1 || found[0].name != "commit" || !found[0].userValue {
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

func TestDiscoverUsesPrecedenceAndOnlyExplicitTrue(t *testing.T) {
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
