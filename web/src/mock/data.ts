/**
 * Deterministic fake catalogue for the mock backend (development and tests
 * only; never shipped). Titles are invented; posters are generated SVGs.
 */
import type { TitleCard } from "../lib/contract";

export interface FakeTitle extends TitleCard {
  popularity: number;
  votes: number;
  released: string;
  addedAt: string;
  country: string;
  language: string;
  people: number[];
  keywords: number[];
  studio: number;
  network?: number;
  collectionId?: number;
  providers: number[];
}

export const GENRES = [
  "Action",
  "Adventure",
  "Animation",
  "Comedy",
  "Crime",
  "Documentary",
  "Drama",
  "Family",
  "Fantasy",
  "History",
  "Horror",
  "Music",
  "Mystery",
  "Romance",
  "Science Fiction",
  "Thriller",
  "War",
  "Western",
];

export const PEOPLE = [
  { id: 101, name: "Mara Ellison", role: "Director" },
  { id: 102, name: "Theo Varga", role: "Actor" },
  { id: 103, name: "Ines Okafor", role: "Actor" },
  { id: 104, name: "Jonah Reyes", role: "Director" },
  { id: 105, name: "Priya Castell", role: "Actor" },
  { id: 106, name: "Lucas Brandt", role: "Actor" },
  { id: 107, name: "Wen Hollis", role: "Director" },
  { id: 108, name: "Ada Moreau", role: "Actor" },
  { id: 109, name: "Kofi Lindqvist", role: "Actor" },
  { id: 110, name: "Sasha Petrov", role: "Writer" },
];

export const KEYWORDS = [
  { id: 201, name: "time travel" },
  { id: 202, name: "heist" },
  { id: 203, name: "small town" },
  { id: 204, name: "space station" },
  { id: 205, name: "found family" },
  { id: 206, name: "haunted house" },
  { id: 207, name: "road trip" },
  { id: 208, name: "courtroom" },
  { id: 209, name: "dystopia" },
  { id: 210, name: "coming of age" },
];

export const STUDIOS = [
  { id: 301, name: "Northlight Pictures" },
  { id: 302, name: "Harbor & Pine" },
  { id: 303, name: "Blue Meridian" },
  { id: 304, name: "Kestrel Films" },
  { id: 305, name: "Lantern House" },
];
export const NETWORKS = [
  { id: 401, name: "Channel Nine" },
  { id: 402, name: "Atlas TV" },
  { id: 403, name: "Sundial" },
];
export const COLLECTIONS = [
  { id: 501, name: "The Meridian Saga" },
  { id: 502, name: "Night Shift Trilogy" },
  { id: 503, name: "Little Engines" },
];
export const PROVIDERS = [
  { id: 601, name: "Streamly" },
  { id: 602, name: "Flixwave" },
  { id: 603, name: "Cinebox" },
];

const ADJ = [
  "Silent",
  "Crimson",
  "Last",
  "Hidden",
  "Electric",
  "Paper",
  "Golden",
  "Distant",
  "Broken",
  "Midnight",
  "Northern",
  "Glass",
  "Wild",
  "Quiet",
  "Iron",
  "Hollow",
  "Bright",
  "Lost",
  "Velvet",
  "Salt",
];
const NOUN = [
  "Harbor",
  "Orchard",
  "Signal",
  "Kingdom",
  "River",
  "Lantern",
  "Frontier",
  "Garden",
  "Witness",
  "Machine",
  "Summer",
  "Archive",
  "Comet",
  "Passage",
  "Empire",
  "Echo",
  "Tide",
  "Circus",
  "Atlas",
  "Hours",
];

function rng(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export function hash(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
  return h >>> 0;
}

function svgData(svg: string): string {
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;
}

function esc(s: string): string {
  return s.replace(
    /[&<>"]/g,
    (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c] as string,
  );
}

/** A generated poster: gradient + title, deterministic per key. */
export function posterFor(key: string, title: string, year?: number): string {
  const h = hash(key) % 360;
  const words = title.split(" ");
  const lines: string[] = [];
  for (const w of words) {
    const last = lines[lines.length - 1];
    if (last && (last + " " + w).length <= 12) lines[lines.length - 1] = `${last} ${w}`;
    else lines.push(w);
  }
  const text = lines
    .slice(0, 4)
    .map(
      (l, i) =>
        `<text x="20" y="${210 + i * 30}" font-family="Georgia,serif" font-size="26" font-weight="700" fill="white">${esc(l)}</text>`,
    )
    .join("");
  return svgData(
    `<svg xmlns="http://www.w3.org/2000/svg" width="200" height="300" viewBox="0 0 200 300"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="hsl(${h},55%,42%)"/><stop offset="1" stop-color="hsl(${(h + 50) % 360},60%,14%)"/></linearGradient></defs><rect width="200" height="300" fill="url(#g)"/><circle cx="${40 + (hash(key + "c") % 120)}" cy="90" r="${38 + (hash(key) % 30)}" fill="white" fill-opacity="0.12"/>${text}<text x="20" y="285" font-family="Helvetica,Arial" font-size="13" fill="white" fill-opacity="0.7">${year ?? ""}</text></svg>`,
  );
}

export function backdropFor(key: string, title: string): string {
  const h = hash(key) % 360;
  return svgData(
    `<svg xmlns="http://www.w3.org/2000/svg" width="640" height="360" viewBox="0 0 640 360"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="0.6"><stop offset="0" stop-color="hsl(${(h + 30) % 360},45%,30%)"/><stop offset="1" stop-color="hsl(${h},55%,10%)"/></linearGradient></defs><rect width="640" height="360" fill="url(#g)"/><circle cx="480" cy="140" r="120" fill="white" fill-opacity="0.08"/><text x="36" y="320" font-family="Georgia,serif" font-size="34" font-weight="700" fill="white" fill-opacity="0.85">${esc(title)}</text></svg>`,
  );
}

export function avatarFor(name: string): string {
  const h = hash(name) % 360;
  const initials = name
    .split(" ")
    .map((p) => p[0])
    .join("")
    .slice(0, 2);
  return svgData(
    `<svg xmlns="http://www.w3.org/2000/svg" width="96" height="96"><rect width="96" height="96" fill="hsl(${h},50%,40%)"/><text x="48" y="60" text-anchor="middle" font-family="Helvetica,Arial" font-size="36" fill="white">${esc(initials)}</text></svg>`,
  );
}

const RATINGS_MOVIE = ["G", "PG", "PG-13", "R"];
const RATINGS_TV = ["TV-Y7", "TV-PG", "TV-14", "TV-MA"];
const COUNTRIES = ["US", "GB", "FR", "KR", "JP", "CA", "DE"];
const LANGS = ["en", "en", "en", "fr", "ko", "ja", "de"];

export function buildCatalog(count = 180, now = new Date("2026-10-19T12:00:00Z")): FakeTitle[] {
  const r = rng(42);
  const out: FakeTitle[] = [];
  const seen = new Set<string>();
  for (let i = 0; out.length < count; i++) {
    const title = `${r() < 0.35 ? "The " : ""}${ADJ[Math.floor(r() * ADJ.length)]} ${NOUN[Math.floor(r() * NOUN.length)]}`;
    if (seen.has(title)) continue;
    seen.add(title);
    const isShow = r() < 0.32;
    const key = String(isShow ? 2_000_000 + i : 1_000_000 + i);
    const year = 1972 + Math.floor(r() * 55);
    const g1 = GENRES[Math.floor(r() * GENRES.length)] as string;
    const g2 = GENRES[Math.floor(r() * GENRES.length)] as string;
    const genres = g1 === g2 ? [g1] : [g1, g2];
    const relDate = new Date(Date.UTC(year, Math.floor(r() * 12), 1 + Math.floor(r() * 27)));
    if (relDate > now) relDate.setUTCFullYear(now.getUTCFullYear() - 1);
    const added = new Date(now.getTime() - Math.floor(r() * 400) * 86400000);
    const ci = Math.floor(r() * COUNTRIES.length);
    out.push({
      key,
      type: isShow ? "show" : "movie",
      title,
      year: relDate.getUTCFullYear(),
      summary: `A ${genres.join(" / ").toLowerCase()} story about the ${title.toLowerCase().replace(/^the /, "")}.`,
      poster: posterFor(key, title, relDate.getUTCFullYear()),
      backdrop: backdropFor(key, title),
      rating: Math.round((5 + r() * 4.2) * 10) / 10,
      contentRating: (isShow ? RATINGS_TV : RATINGS_MOVIE)[Math.floor(r() * 4)],
      runtimeMinutes: isShow ? 25 + Math.floor(r() * 35) : 82 + Math.floor(r() * 90),
      genres,
      popularity: Math.round(r() * 300 * 10) / 10,
      votes: Math.floor(r() * 25000),
      released: relDate.toISOString().slice(0, 10),
      addedAt: added.toISOString(),
      country: COUNTRIES[ci] as string,
      language: LANGS[ci] as string,
      people: [
        PEOPLE[Math.floor(r() * PEOPLE.length)]!.id,
        PEOPLE[Math.floor(r() * PEOPLE.length)]!.id,
      ],
      keywords: [KEYWORDS[Math.floor(r() * KEYWORDS.length)]!.id],
      studio: STUDIOS[Math.floor(r() * STUDIOS.length)]!.id,
      network: isShow ? NETWORKS[Math.floor(r() * NETWORKS.length)]!.id : undefined,
      collectionId:
        !isShow && r() < 0.1 ? COLLECTIONS[Math.floor(r() * COLLECTIONS.length)]!.id : undefined,
      providers: PROVIDERS.filter(() => r() < 0.35).map((p) => p.id),
    });
  }
  return out;
}

export function card(t: FakeTitle): TitleCard {
  return {
    key: t.key,
    type: t.type,
    title: t.title,
    year: t.year,
    summary: t.summary,
    poster: t.poster,
    backdrop: t.backdrop,
    rating: t.rating,
    contentRating: t.contentRating,
    runtimeMinutes: t.runtimeMinutes,
    genres: t.genres,
  };
}
