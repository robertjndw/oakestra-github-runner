# GitHub runners addon UI

Dashboard UI for `oakestra-github-runner`, a React micro-frontend that the Oakestra dashboard
loads with Module Federation. The Go backend embeds the built `dist/` and serves it next to its
`/api`, so there is no separate UI container.

Pages: overview (setup checklist, runner counts, `runs-on` snippets), runners, jobs, pools and
settings (GitHub connections, GitHub App wizard, token form, Oakestra service user). It also adds
a "busy runners" tile to the dashboard overview and a runner list to the cluster page.

## Install

`@oakestra/*` come from GitHub Packages, which wants a token even for public packages. pnpm
ignores tokens in a repo `.npmrc`, so set one in your user config once, for example
`pnpm config set //npm.pkg.github.com/:_authToken "$(gh auth token)"`, or export
`NODE_AUTH_TOKEN` (the Makefile and Dockerfile of this repo do the latter).

```sh
pnpm install
```

## Checks

```sh
pnpm typecheck
pnpm test
pnpm build      # also fails when a class lacks the ghr: prefix
```

## Addon id

The dashboard refuses a bundle whose id differs from the name the addons engine reports. A
locally registered addon is reported as `local--github-runner`, so build for that name with:

```sh
ADDON_ID=local--github-runner pnpm build
```

Without `ADDON_ID` the id is `github-runner`. The code always reads the id from
`virtual:oakestra-addon`, never from a literal. `make ui` in the repository root passes its own
`ADDON_ID` through.

## Development against a real backend

The addon calls its backend at `/addon-assets/<id>/api/...` on the dashboard origin. In
production the dashboard's nginx forwards that to the container. In development the dashboard's
Vite server does it with its `ADDON_ASSETS` mirror (`apps/shell/vite.config.ts`), and this
addon's dev server forwards `/api` to the Go backend (`vite.config.ts`, override the target with
`GHR_BACKEND`). So the request path is:

```text
browser -> dashboard :5173 /addon-assets/github-runner/api/x
        -> addon dev server :5181 /api/x   (ADDON_ASSETS strips /addon-assets/github-runner)
        -> Go backend :8080 /api/x         (Vite proxy)
```

Run these in three terminals.

1. Go backend, from the repository root. No auth, so the mock dashboard login works:

   ```sh
   GHR_AUTH=off GHR_LISTEN=:8080 GHR_DATA_DIR=/tmp/ghr-data go run ./cmd/ghrunner
   ```

   The backend listens on `:80` unless `GHR_LISTEN` says otherwise, and the addon dev server
   expects port 8080. For another port use `GHR_BACKEND=http://127.0.0.1:<port> pnpm dev`. Set
   `OAKESTRA_API_URL` to a reachable system manager if you want clusters and runners to work,
   see `internal/config/config.go` for the other variables.

2. This addon's dev server on port 5181:

   ```sh
   cd ui
   pnpm dev
   ```

3. The dashboard with the mock API in the browser (log in as `Admin` / `Admin`, or `alice` and
   so on with their name as password to see the non-admin view). From the `dashboard-v2`
   repository:

   ```sh
   ADDON_ASSETS=github-runner=http://localhost:5181 pnpm dev:mock
   ```

Then open:

```text
http://localhost:5173/?addon-override=github-runner=http://localhost:5181/mf-manifest.json
```

The override is kept in `localStorage`. Remove it with `?addon-override=github-runner=`.

Notes:

- The override name must equal the addon id, so do not set `ADDON_ID` for the dev server unless
  you use the same name in the override and in `ADDON_ASSETS`.
- `ADDON_ASSETS` is a dev-server setting of the dashboard. Without it, calls to the backend
  answer 404 with a hint.
- The GitHub App wizard sends the browser to GitHub and back to the URL of the settings page
  (`http://localhost:5173/addons/github-runner/settings` in development). It has only been run
  against fake backends in the tests, not against GitHub.

## Layout

```text
src/index.tsx             defineAddon: routes, nav, widgets; pages are lazy() chunks
src/addon.css             Tailwind with prefix(ghr), tokens come from the shell
src/api/                  types (mirror of internal/core), fetch client, TanStack Query hooks
src/pages/                Overview, Runners, Jobs, Pools (+ PoolDialog), Settings
src/settings/             connections list, GitHub App wizard, token form, Oakestra user
src/widgets/              home stat tile and cluster page card
src/components/           badges, permission helpers, error alert
src/lib/                  pool validation and name proposal, formatting, manifest form post
src/test/                 test setup and a fake addon backend
```

Every Tailwind class carries the `ghr:` prefix, including those passed to `@oakestra/ui`
components. There is no global CSS.
