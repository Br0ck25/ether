# Ether web UI

React + TypeScript + Vite SPA for Ether's setup wizard and admin pages.
Served by the Go binary from the embedded `internal/admin/dist` build.

```sh
pnpm install
pnpm dev      # Vite dev server on :5173, proxies /ether/api to :32400
pnpm test     # Vitest component tests
pnpm build    # emits to ../internal/admin/dist (committed)
```

Commit the build output: `go build` embeds it, so the server must work
from a checkout without Node. `internal/admin/dist/.gitkeep` keeps the
directory in git when empty; the Go handler falls back to a "UI not built"
page when `index.html` is absent.
