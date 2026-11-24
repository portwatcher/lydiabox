# Contributing to LydiaBox

## Build

You need Go (the version in `go.mod`), Node.js 22 or later for `web/`, and
Docker for the guest images.

```sh
make build                 # dist/lydiabox and dist/lydiabox-guest
cd web && npm ci && npm run build
```

All Go binaries build with `CGO_ENABLED=0`. Do not add CGO dependencies.

## Test gates

Run these checks before you send a pull request:

```sh
CGO_ENABLED=0 go test -race ./...
go vet ./...
cd web && npm run typecheck && npm run lint && npm run test
```

For a change to the `chv` driver or the guest images, also run the
[KVM checklist](docs/kvm-test.md). A change to documentation only needs
correct paths, links, and content.

## Code rules

The "Architecture and code constraints" section of [AGENTS.md](AGENTS.md)
has the full rules. In short:

- Keep the `tenantID` parameter in the internal services.
- Keep upstream LLM keys, refresh tokens, and connector tokens on the
  host. Never write them to the guest disk. The one exception is
  guest-native mode, where the harness keeps the user's own sign-in in the
  user's VM (see [docs/design.md](docs/design.md) section 4.4.2).
- Sandbox every host file path (`filepath.Clean` and a root boundary
  check, or `os.Root`).
- Keep source files below 600 lines (800 lines is the maximum).
- In TypeScript, do not use `any`, `as unknown`, or `@ts-ignore` when a
  safer type is possible. Put braces around all conditional bodies.
- Update `README.md` or `docs/` when a change makes them outdated.

## Pull requests

Fork the repository, make your change on a branch, and open a pull request
against `main`. The maintainer reviews and merges it. The "Maintainer agent
sessions" rules in [AGENTS.md](AGENTS.md) (direct pushes to `main`, worktree
cleanup, task archival) do not apply to contributors.

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org/), for
example `fix(harness): attach a watched terminal again after a link loss`.
Keep each commit to one logical change. Do not commit credentials or
built binaries.

## License

LydiaBox is licensed under the [Apache License 2.0](LICENSE). By sending a
contribution, you agree that it is licensed under the same terms.
