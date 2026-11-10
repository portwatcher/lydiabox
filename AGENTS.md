# Project guidance

## Response requirements

- 回答严格遵守 ASD-STE100
- Explicit user instructions take precedence over repository and skill guidance,
  subject to system/developer constraints. Apply skills only when their workflow
  helps the requested task; read supporting references as needed.
- Continue authorized implementation, validation, integration, and cleanup without
  asking again for routine steps. If a rule blocks progress, cite its file and
  exact instruction, explain the concrete blocker, and continue independent work.

## Architecture and code constraints

- LydiaBox (`lydiabox`) is a self-hostable, serverless-style personal agent VM
  and harness orchestration platform in Go with an embedded React + TypeScript
  web workstation (`web/`). Read [docs/design.md](docs/design.md) for the system
  architecture, subsystem specifications, and phased verification gates.
- Enforce `CGO_ENABLED=0` across all Go binaries (`cmd/lydiabox` and
  `cmd/lydiabox-guest`). Never link native libraries via CGO.
- Preserve the multi-tenant `tenantID string` parameter across internal services
  (`storage`, `vmm`, `harness`, `connector`), defaulting to `"default"` in
  single-user self-hosted mode.
- Support both VMM backends: `internal/vmm/chv` (Cloud Hypervisor + `virtiofsd`
  on Linux KVM) and `internal/vmm/docker` (container fallback when `/dev/kvm` is
  absent).
- Run `juicefs mount`, `magpie`, and AES-256-GCM encrypted connector/OAuth state
  strictly on the trusted host control plane. Never write upstream LLM keys or
  refresh tokens to the guest disk.
- Enforce strict path-traversal sandboxing (`filepath.Clean` + root boundary
  verification / `os.Root`) on all host File Manager APIs.
- Prefer source files below 600 lines; 800 lines is the maximum. This applies to
  Go, tests, TypeScript/React, HTML, CSS, and scripts. Split oversized source
  files into cohesive domain modules instead of `utils/_` or `utils._`
  abstractions. Configuration files are exempt.
- Avoid `as any`, `: any`, `as unknown`, and `@ts-ignore` in TypeScript unless
  safer alternatives have been exhausted. Use braces around conditionals,
  including returns.
- For `web/` interface work, use the repository skills in
  [.agents/skills](.agents/skills/README.md) (`better-interface` for a combined
  review and the relevant `better-*` domain skill for a focused change). Avoid
  unnecessary secondary labels and descriptions. Let the layout do the
  explaining.
- Update `README.md` or `docs/` when a change makes their content outdated.

## Worktrees, branches, commits, integration, and cleanup

- Before changing tracked files, fetch `origin` and create a dedicated
  Codex-managed worktree from the latest `origin/main`, unless the user
  explicitly asks to use the primary checkout. Do not make task changes in the
  primary checkout or reuse a worktree that belongs to another active task.
- Create task worktrees under the Codex-managed worktree root, never under an
  operating-system temporary directory. Start them on detached `HEAD`, then
  create a task-specific branch when work is ready to begin. Use the
  `codex/<short-description>` prefix unless the user requests another name.
  Never check out the shared `main` branch in a task worktree.
- Preserve unrelated user changes. Do not stage, overwrite, commit, or remove
  another task's files, branch, worktree, or active processes.
- Validate the behavior affected by the change (`CGO_ENABLED=0 go test -race ./...`,
  `go vet ./...`, and relevant `web/` type/build checks). Documentation-only or
  instruction-only edits need path, link, and content checks, not application
  builds. Rerun checks after relevant changes or failures.
- Use focused conventional commits. When a task has multiple independently
  complete parts, commit each logical portion separately. Add a
  `Co-authored-by` trailer identifying the coding agent and base model. Keep
  credentials and generated binaries out of commits.
- A coding session is not complete when finished changes exist only in a
  worktree, commit, task branch, or pull request. When the job is done, the
  agent must integrate the task branch into the latest remote `main`, push the
  updated `main`, and verify that remote `main` contains the task commit before
  ending the session.
- Before integration, fetch the remote, incorporate the latest `origin/main`
  into the task branch, resolve conflicts, and run the appropriate validation.
  Push with `git push origin HEAD:main`. If another session moved `main` first,
  incorporate the new state and push again. Never force-push `main`.
- If the task cannot be merged safely because of ambiguous conflicts, failing
  checks, unresolved requirements, missing permission, or another genuine
  blocker, preserve the branch and worktree and report the exact blocker to a
  human. Do not force the merge, guess at the resolution, or delete work that is
  still needed; the human decides how to proceed.
- If unfinished work must continue beyond the current session, open or fork a
  new Codex session before ending the current one and hand off the exact
  branch, worktree, completed work, remaining work, and validation state. Do
  not silently abandon an unfinished session or its artifacts.
- After verifying that the task commit is contained in remote `main`, clean up
  all task artifacts in the same session: confirm the worktree is clean and
  inactive, remove it, delete the local task branch, delete the remote task
  branch, and prune stale worktree metadata. Never delete the protected `main`
  branch.
- Delete an unmerged branch, and its worktree, when you confirm that its work is
  no longer needed, for example because `main` already has equivalent changes.
  This confirmation is enough; a human does not need to approve the deletion.
  Never delete an unmerged branch whose work is still needed.
- At the start and end of a coding session, audit managed worktrees and local
  and remote task branches. Remove clean, unlocked worktrees with no process
  inside and task branches already merged into `origin/main` or confirmed as no
  longer needed, and report any other unmerged branch to a human.

## Automatically archive tasks with missing working directories

- At session start, session completion, and after worktree removal, check all
  accessible Codex tasks across projects for `Current working directory missing`
  or a confirmed nonexistent working directory on the owning host.
- Automatically archive every matching task, regardless of completion status.
  The user explicitly authorizes this rule: completion checks, recovery,
  handoff, and additional confirmation are not prerequisites for archival when
  the working directory is confirmed missing. Use `set_thread_archived` with
  `archived: true`, the exact task ID, and owning host.
- This rule is specifically about Codex task archival. Preserve chat history
  and any surviving files or branches; do not delete them, recreate directories,
  or start replacement tasks as part of this action. Git artifact deletion
  remains subject to its separate cleanup checks.
- Verify each archival succeeded. An unavailable host or a permission error is
  not proof of a missing directory; report lookup or archival failures without
  claiming success. Apply this rule to all matching tasks across projects.

## One-time operations run locally

- Never create, dispatch, or repurpose GitHub Actions for one-time or temporary
  operations and maintenance, including ad hoc diagnostics, credential
  maintenance, data or registry migrations, and cleanup.
- Execute these operations directly from the local computer with the documented
  CLI tools and approved local credentials.
