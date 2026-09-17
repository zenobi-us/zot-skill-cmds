# zot-skill-cmds

A Go extension for [zot](https://github.com/patriceckhart/zot) that exposes selected skills as short slash commands.

## Mark a skill

Set `user-invocable: true` in a skill's frontmatter:

```yaml
---
name: developer:commit
description: Create a clean commit for the current changes.
user-invocable: true
---

Follow the commit workflow described here.
```

The extension registers the final name component as a command:

```text
/commit review the staged diff
```

The canonical zot command remains available:

```text
/skill:developer:commit review the staged diff
```

## Three-state behavior

The extension intentionally treats `user-invocable` as a three-state alias marker:

| Frontmatter | Bare command |
| --- | --- |
| omitted | not registered |
| `user-invocable: true` | registered |
| `user-invocable: false` | not registered |

This is an extension policy. It does not change Claude Code's own default behavior for an omitted field.

## Discovery

The extension scans extension-declared skill roots first, matching zot's extension precedence:

1. `./.zot/extensions/*/skills` from each enabled `extension.json`
2. `$ZOT_HOME/extensions/*/skills` from each enabled `extension.json`
3. `./.zot/skills`
4. `$ZOT_HOME/skills`
5. `./.claude/skills`
6. `~/.claude/skills`
7. `./.agents/skills`
8. `~/.agents/skills`

This lets the extension alias skills bundled by installed zot skill extensions, such as `/commit` for a skill with `user-invocable: true`. Symlinked extension directories are supported. Only files named `SKILL.md` are considered. Duplicate canonical skill names keep the first match. The extension reloads a skill body when its command runs, so content edits do not need an extension restart.

The current implementation uses a small frontmatter parser and reads `name`, `description`, and `user-invocable`. Unknown fields are ignored.

## Install and test

Build the extension:

```sh
go build -o zot-skill-cmds .
```

Run it for one zot session:

```sh
zot --ext .
```

Or install it:

```sh
zot ext install .
```

Run tests:

```sh
go test ./...
```

## Collision behavior

Aliases use zot's normal extension command namespace. Built-in zot commands retain priority. If multiple discovered skills produce the same alias, this extension keeps the first skill according to discovery precedence and logs a diagnostic.

The extension process has the same filesystem permissions as the user. Skill files are instruction text; review them before enabling aliases.
