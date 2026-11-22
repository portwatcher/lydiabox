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

[AGENTS.md](AGENTS.md) has the full rules. In short:

- Keep the `tenantID` parameter in the internal services.
- Keep upstream LLM keys, refresh tokens, and connector tokens on the
  host. Never write them to the guest disk.
- Sandbox every host file path (`filepath.Clean` and a root boundary
  check, or `os.Root`).
- Keep source files below 600 lines (800 lines is the maximum).
- In TypeScript, do not use `any`, `as unknown`, or `@ts-ignore`.
- Update `README.md` or `docs/` when a change makes them outdated.

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org/), for
example `fix(harness): attach a watched terminal again after a link loss`.
Keep each commit to one logical change. Do not commit credentials or
built binaries.

## License

LydiaBox is licensed under the [Apache License 2.0](LICENSE). By sending a
contribution, you agree that it is licensed under the same terms.
