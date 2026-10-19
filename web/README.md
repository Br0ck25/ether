# Ether owner UI

The `/ether/` web app for the server owner: setup wizard, Home dashboard,
Collection Studio, Home Designer, Viewers, Discovery, Live TV, Insights,
Libraries & backups, Playback, Audio & subtitles, Safety, Settings and traces. Viewers never see it; they only use Plex. The API contract is
[`docs/owner-ui.md`](../docs/owner-ui.md).

Built with React 19, react-router, Vite and the AppKit design system
(`@braedonsaunders/appkit-ui` + `@braedonsaunders/appkit-tokens`, Tailwind v4).
Colours come from semantic tokens only; dark is the default theme. AppKit owns
the theme provider/toggle and pre-paint script, shared through `src/theme.ts`.

```sh
pnpm install
pnpm dev         # Vite on :5173, proxies /ether/api to http://localhost:32400 (override with ETHER_API=...)
pnpm dev:mock    # Vite on :5173 against the in-browser fake backend (src/mock); no server needed
pnpm typecheck
pnpm test        # Vitest + Testing Library, offline, against the same fake backend
pnpm build       # emits to ../internal/admin/dist (committed; go:embed serves it)
```

Layout:

| Path | What |
| --- | --- |
| `src/lib/api.ts` | Typed client for every endpoint (auth header, CSRF header, error shape) |
| `src/lib/contract.ts` | The owner-UI contract types |
| `src/lib/rules.ts`, `layout.ts`, `health.ts`, `history.ts` | Pure logic: rule editing, layout rules, health summaries, undo/drafts (unit-tested) |
| `src/components/` | Shared pieces: artwork (auth-aware lazy images), posters/shelves, pickers, charts, fields |
| `src/pages/` | One folder or file per screen; heavy screens are code-split |
| `src/mock/` | Fake backend for `--mode mock` and tests. Only reachable behind `import.meta.env.MODE === "mock"`, so production builds drop it |
| `src/test/` | Flow tests (sign-in, wizard gating, studio editing and preview, layout rules, viewer overrides, settings) |

Commit the build output: `go build` embeds it, so the server must work
from a checkout without Node. `public/.gitkeep` is copied into the output
so `internal/admin/dist` always exists for `go:embed`; the Go handler
falls back to a "UI not built" page when `index.html` is absent.

Rule values (the contract types them "by field"): `in`/`notIn` take an
array (facet keys as strings, lookup ids as numbers), `is`/`isNot` a single
value, `gte`/`lte` a number or `YYYY-MM-DD`, `between` a `[min, max]` pair,
and `within` a relative window such as `"30d"`, `"12w"`, `"6m"`, `"1y"`.
Lookup-backed rules carry the display names in `label`, joined by `", "`.
The layered Home document uses one-based `slot`, `minSlot` and `maxSlot`;
legacy zero-based layout rules are migrated. Taste overrides use the genre name,
keyword name, or TMDB person id (decimal string) as `value`.
