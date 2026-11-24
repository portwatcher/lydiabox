---
name: lydiabox-cli-hub
description: Find and install agent-native command-line interfaces for desktop applications and online services with cli-hub (CLI-Anything). Use when a task needs an application such as GIMP, Inkscape, LibreOffice, Blender, Kdenlive, or a service CLI, and no suitable command is installed yet.
---

<!-- Managed by LydiaBox; replaced at each harness start. -->

# cli-hub (CLI-Anything) in the LydiaBox VM

Adapted from `cli-hub-meta-skill/SKILL.md` of CLI-Anything
(https://github.com/HKUDS/CLI-Anything), Apache License 2.0. Changed for
LydiaBox.

`cli-hub` is already installed in this VM. Do not install it again with
pip. If the command is not found, this VM image does not have it (a slim
image or an older image).

A hub CLI gives an application a command line with JSON output (`--json`)
and a REPL. Most hub CLIs are named `cli-anything-<name>`.

## Find a CLI

```bash
cli-hub search image            # search by name, description, or category
cli-hub list -c <category>      # list one category; `cli-hub list` lists all
cli-hub info gimp               # requirements, entry point, install method
cli-hub can "transcribe audio"  # find a capability in the workflow matrices
```

Read the `Requires` line of `cli-hub info` before you install.

## Install a CLI

```bash
cli-hub install gimp
cli-anything-gimp --help
```

- A hub CLI usually controls a real application. Install the application
  too, for example `sudo apt-get install -y gimp`. Read the skill
  `lydiabox-install-software` first: it tells which installs persist.
- `cli-hub install` runs as your user. Python CLIs go to `~/.local`, npm
  CLIs go to `~/.local` (the `cli-hub` command sets the npm prefix), and
  uv CLIs go to `~/.local/share/uv`. The commands are in `~/.local/bin`.
  These files are in the home directory and persist.
- Some CLIs use another installer (`brew`, `go`, or a script). These can
  fail in this VM. Then read `cli-hub info <name>` and install the
  program in a different way.
- `cli-hub list` and `cli-hub info` show the installed CLIs.
  `cli-hub uninstall <name>` removes one.

## Workflow matrices

A matrix maps the steps of a workflow (for example `video-creation`) to
CLIs. Check first, then install only what the task needs:

```bash
cli-hub matrix list
cli-hub matrix preflight video-creation --json      # exit 3: gaps
cli-hub matrix install video-creation --capability text.transcribe --dry-run
cli-hub matrix install video-creation --capability text.transcribe
```

Scope every install with `--capability <id>`, `--recipe <id>`, or
`--only a,b`. Do not install a full matrix for a task that needs one
capability. After an install, read the matrix `SKILL.md` that cli-hub
writes. Exit codes: `0` ok, `3` gaps, `1` failure, `2` usage error.

## Rules

- Install only the CLIs that the current task needs.
- Do not unset `CLI_HUB_NO_ANALYTICS`. LydiaBox sets it to `1`, so cli-hub
  sends no usage events.
- Use `--json` output when you read the result of a hub CLI.
