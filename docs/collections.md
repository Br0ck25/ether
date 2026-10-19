# Collections

Ether builds collections for you and publishes them to Plex: they show up in
the **Collections** tab of your Movies and TV sections with their own poster
art, and you can promote any of them to a **Home row** or a **section row**
for the people you choose, at the times you choose. Viewers never see Ether —
they just see a Plex library that looks curated by hand.

You design collections in the **Collection Studio** at `/ether/`: start from
one of the curated templates, describe what you want in plain English, or
build it yourself from sources and rules while the result updates live with
posters as you edit.

## How a collection works

A collection is a recipe, evaluated by Ether:

1. **Sources** gather candidate titles (the whole catalogue, a TMDB list, a
   director's filmography, a public Letterboxd list…). Several sources are
   combined.
2. **Rules** filter the candidates — *all* of them must hold, or *any* one —
   and **exclusions** remove titles.
3. The result is **sorted**, optionally **limited**, and can **rotate** a
   different subset into view every day or week.
4. Ether keeps it up to date on a **schedule** (hourly, daily, weekly) or
   evaluates it **live** whenever Plex asks.

Titles a source finds outside your catalogue (a film on a Trakt list that
isn't popular enough to be in the browsable set, say) join the catalogue when
the collection is saved, so every member can be browsed and played. Previews
never change your library.

Like the library itself, collections hold released titles only — unless you
show unreleased titles in Settings, or the collection asks for future
releases (a *Coming Soon* list, or a release-date rule reaching into the
future).

## Sources

| Source | What it adds |
| --- | --- |
| **Catalogue** | Every browsable title. Combine with rules ("80s sci-fi under two hours"). |
| **TMDB list** | Trending (day/week), Popular, Top Rated, Now Playing, Upcoming, Airing Today, On The Air. |
| **TMDB collection** | A franchise, e.g. *Star Wars Collection*. |
| **TMDB keyword / company / network** | Everything TMDB tags with a keyword, from a studio (*Pixar*, *A24*), or from a TV network (*HBO*). |
| **TMDB person** | A filmography: as an actor, director, writer, or any credit. Talk-show guest spots are left out. |
| **Streaming service** | What's streaming on Netflix, Disney+, Prime Video… in your region (or one you pick). |
| **TMDB discover** | Any TMDB *discover* query. Ids can be given as names: `with_keywords: "christmas\|holiday"`, `with_genres: "Animation"`. |
| **Trakt list** | A public Trakt list (`https://trakt.tv/users/<user>/lists/<list>`). Needs a Trakt client ID in Settings. |
| **Letterboxd list** | A public Letterboxd list (`https://letterboxd.com/<user>/list/<list>/`). |
| **MDBList** | A public MDBList list (`https://mdblist.com/lists/<user>/<list>`). |
| **Hand-picked** | Titles you choose. |

You can name a TMDB entity instead of looking up its id; Ether resolves the
name once and remembers it.

External lists are fetched politely: Ether identifies itself, spaces requests
to each site, caches pages for six hours, and remembers how every list item
matched (by TMDB id, IMDb id, exact title and year, or — for Letterboxd — the
film's page, at most 25 new look-ups per refresh). Items that cannot be
matched are reported as warnings on the collection.

## Rules

Each rule is a **field**, an **operator** and a **value**.

| Field | Values | Operators |
| --- | --- | --- |
| `genre` | TMDB genre names (`"Science Fiction"`) | `is` (has all), `in` (any of), `isNot` / `notIn` (none of) |
| `keyword` | TMDB keywords by name or id | same |
| `person` | People by id or name; add `"role": "cast" \| "director" \| "writer" \| "crew"` | same |
| `studio`, `network` | Production companies and networks | same |
| `country` | ISO codes (`"KR"`) or names | same |
| `language` | Original language, ISO 639-1 (`"ko"`) | same |
| `contentRating` | `"PG-13"`, `"TV-14"`… | `is`, `in`, `isNot`, `notIn`, and `lte` / `gte` by severity |
| `collection` | TMDB collection id | `is`, `in`, `isNot`, `notIn` |
| `watchProvider` | Streaming service id/name (optional `"region"`) | `is`, `in`, `isNot`, `notIn` |
| `year`, `decade` | `1985`, `1980` (or `"1980s"`) | `is`, `in`, `isNot`, `notIn`, `gte`, `lte`, `between` (`[1980, 1989]`) |
| `rating` | TMDB rating 0–10 | same |
| `votes`, `popularity` | Numbers | same |
| `runtime` | Minutes | same |
| `released`, `addedAt` | `"2024-05-01"`, `"now"`, offsets like `"-30d"` / `"+3m"` | `gte`, `lte`, `between`, `within` (`"90d"`, `"2y"`) |
| `watched`, `inProgress` | `true` / `false` | `is`, `isNot` — evaluated **per viewer** |

Some facts are only known for titles whose full details Ether has fetched
(keywords, people, studios, runtime, certifications, streaming availability).
For those rules Ether also asks TMDB *discover* for matches across all of
TMDB, so a rule like "runtime under 90 minutes" finds every short film, not
just the ones someone already opened. When a TMDB-backed source (keyword,
company, network, streaming service, discover) is combined with *all*-rules,
the rules are added to that source's query too, so it returns the titles that
match rather than just its most popular ones.

Per-viewer rules (`watched`, `inProgress`) are applied when each person's Plex
asks: "Acclaimed films you haven't seen" is different for everyone.

## Sorting, limits and rotation

Sort by popularity, rating, release date, title, date added, random (stable
for a day), the order of the sources, or **personal** — each viewer's own
predicted taste, from Ether's personalization engine. Set a limit to keep the
top *n*. **Rotate** shows a different subset every day or week: across one
cycle every member is shown exactly once, and within a period everyone sees
the same set.

## Refresh

| Refresh | Behaviour |
| --- | --- |
| `live` | Evaluated when Plex asks, reused for two minutes. Best for catalogue-only rules. |
| `hourly` / `daily` / `weekly` | Rebuilt in the background on that schedule; Plex is always served the last build instantly. |

Saving a collection builds it immediately. **Rebuild** in the Studio forces a
refresh. Each collection reports its item count, when it was last built, and
any warnings (unmatched list items, no matches).

## Artwork

Every collection gets a poster Plex shows in the Collections tab, generated in
pure Go and cached on disk:

- **Collage** — a 2×2 or 3×2 grid of member posters fading into your accent
  colour, with the title set in the Go fonts.
- **Backdrop** — a member's backdrop cropped to portrait with a title
  treatment.
- **Auto** — collage with enough posters, otherwise backdrop, otherwise a
  typographic poster.
- **TMDB** — the TMDB collection's own poster.
- **Custom** — any image URL or TMDB image path.

Pick an accent colour, or let Ether derive one from the artwork. The poster is
regenerated when members, title, mode or accent change. The collection's
background art is its first member's backdrop.

## Publishing

| Target | Effect |
| --- | --- |
| **Collections tab** | Appears under Collections in the matching section(s); mixed collections appear in both. |
| **Home row** | Offered to the personalization engine as a Home row. |
| **Section row** | Offered as a row at the top of the Movies or TV section. |
| **Users** | Everyone, or chosen Plex users. |
| **Schedule** | Days of the week, hours (wrapping past midnight, e.g. 22–2), and devices (TV, mobile, web, desktop) for rows. |

Each viewer's content-rating limit always applies: a PG-limited account never
sees an R-rated member, and a collection with nothing they may watch is hidden
from them.

Ether also lists TMDB franchises in your catalogue (two or more films) and
ships built-in live lists (trending, popular, top rated, now playing,
upcoming, airing today). Built-ins can be retargeted, rotated, re-arted or
disabled, but not deleted.

## Templates

The Studio ships more than 70 templates across: franchises & universes (Star
Wars, Middle-earth, the Wizarding World, the MCU, Bond, Pixar, Studio Ghibli,
A24…), directors (Nolan, Villeneuve, Tarantino, Spielberg, Scorsese, Kubrick,
Miyazaki, Bong Joon-ho, Wes Anderson, Gerwig), actors, decades (70s–2010s),
critically acclaimed (Top 250, Hidden Gems, Modern Classics, Acclaimed
Series), moods (Cosy Nights In, Mind-Bending, Feel-Good, Edge of Your Seat,
Tearjerkers, Date Night, Epic Weekend, Short on Time), streaming services
(what's on Netflix, Disney+, Prime Video, HBO Max, Apple TV, Hulu,
Paramount+), seasonal (Halloween, Christmas, Valentine's Day), world cinema
(Korean, Japanese, anime, French, Spanish-language, Bollywood, Nordic Noir,
K-dramas), family, TV (limited series, sitcoms, prestige drama, docuseries),
new & upcoming, and per-viewer picks. Every template is a complete collection
you can save as is or edit.

## Describe it in plain English

With an LLM configured (Settings → LLM), type what you want — *"80s sci-fi
under 2 hours that critics loved"* — and Ether drafts the collection: sources,
rules, sort and limit. People, keywords, studios and services it names are
matched to TMDB, anything invalid is dropped with a note, and you get an
explanation of how the rules capture the request. Review it in the Studio
before saving.

## Owner API

All under `/ether/api/` (see `docs/owner-ui.md` for the full contract):

| Method | Path | |
| --- | --- | --- |
| GET / POST | `collections` | List (owner collections, then built-ins) / create |
| GET / PUT / DELETE | `collections/{id}` | One collection |
| POST | `collections/preview` | Evaluate an unsaved collection: total, first 60 titles, warnings |
| POST | `collections/{id}/rebuild` | Rebuild now |
| GET | `collections/templates` | Curated templates |
| POST | `collections/generate` | Draft from a prompt (501 when no LLM) |
| GET | `collections/{id}/artwork?w=` | The poster |
| GET | `catalog/search`, `catalog/facets`, `catalog/lookup`, `catalog/title/{key}` | Pickers for the builder |
| GET | `image?path=&w=` | TMDB artwork proxy (TMDB image paths only, cached) |

Artwork and the image proxy are readable without the owner token so `<img>`
tags can load them; the owner UI is reachable from the local network only.

## How it compares to Kometa

Kometa (formerly Plex Meta Manager) is a fine tool for curating a Plex server
that holds files. Ether's collection engine goes further because it *is* the
server:

- **No YAML, no cron.** Collections are designed visually with a live preview
  (under 10 ms over a 50,000-title catalogue once warm), from templates, or
  from a sentence.
- **Collections can contain anything.** Kometa can only collect files you
  already have; Ether's members join the catalogue and stream on demand, so a
  Letterboxd list or a director's filmography is complete, not "12 of 31
  found".
- **Per-viewer collections.** Watched/in-progress rules, personal ordering
  from each viewer's taste profile, content-rating limits, and per-user
  targeting are evaluated for every viewer at request time — something a
  metadata manager writing one shared collection cannot do.
- **Rows on a schedule and per device.** Promote a collection to Home only on
  weekend evenings on the TV, for the people it suits.
- **Rotation built in.** Big collections stay fresh without rebuilding them.
- **Artwork included.** Collage and backdrop posters are generated for every
  collection; no overlay packs or image hosting.
- **Smarter rules.** Rules TMDB can answer are pushed down to TMDB, so
  detail-only facts (runtime, keywords, certifications, streaming services)
  match across all of TMDB, not just titles already scanned.
- **Instant updates.** Saving builds immediately; nothing waits for a nightly
  run or a library rescan.
