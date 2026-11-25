# Agent notes

Oakestra addon that runs GitHub Actions jobs as single-use runner containers. One Go binary
(API, embedded dashboard UI, controller) plus a React UI in `ui/`. Read `README.md` for the
architecture.

## Commands

```sh
go vet ./... && go test -race ./...     # backend
cd ui && pnpm typecheck && pnpm test    # UI
cd ui && pnpm build                     # also enforces the ghr: class prefix
make build                              # UI bundle copied into internal/webui/dist, then the binary
make run                                # backend on :8080, GHR_AUTH=off
```

## Rules of the codebase

- `internal/core` is the contract. It holds the domain types, the interfaces between packages
  and, in a comment in `ports.go`, the HTTP route table. Packages depend on `core`, not on each
  other; only `cmd/ghrunner` wires implementations together. Change the contract first, then
  the implementations, and keep `ui/src/api/types.ts` in step by hand.
- Never log or return secrets: JIT runner configs, tokens, App private keys, the Oakestra
  password. `Pool.LastError` is returned to every authenticated user, so errors stored there
  must not carry them either.
- `Scale` in `internal/controller/scaler.go` returns an error only when redelivering the message
  can fix it. Runner creation problems are recorded on the pool and retried by the reconciler.
  Every step must be idempotent, messages are redelivered.
- Removing a runner that may be idle: remove the GitHub registration first and only delete the
  Oakestra service if that succeeded, otherwise a job that was just assigned gets killed.
- UI: every Tailwind class carries the `ghr:` prefix, no global CSS, backend calls go through
  `useAddonApi()`, the addon id comes from `virtual:oakestra-addon`.

## Oakestra API traps

These are verified against the system manager source and are the reason for much of
`internal/oakestra` and the reconciler:

- No service accounts. The backend logs in as a normal user; access tokens last 10 minutes and
  a refresh returns no new refresh token. Services are only visible to their creator.
- `DEAD` and `FAILED` instances are redeployed forever, every 15 seconds. `one_shot` plus exit
  code 0 gives `COMPLETED`. Finished or crashed runners have to be deleted.
- `POST /api/service/` answers 200 even when the service failed, and `POST .../instance` always
  answers ok. `DELETE` of an unknown service answers 500.
- `cmd` replaces the image's entrypoint and command. `args` is ignored.
- `vcpus` and `memory` only filter scheduling. Only the `direct` constraint is implemented, and
  it matches cluster and node names, not ids. The scheduler ignores architecture for containers.

## Addon engine and dashboard traps

- The Go addons engine gives services no DNS names for each other, bakes `environment` into the
  definition, and serves definitions without authentication. Hence one container and
  credentials entered in the UI.
- The dashboard refuses a UI bundle whose id differs from the engine's addon id, which is
  `local--github-runner` for a local registration. The id is a build argument (`ADDON_ID`).
