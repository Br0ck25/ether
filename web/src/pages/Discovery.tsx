import { ListInput } from "../components/fields";
import { EditPanel } from "../components/EditPanel";
import { Button, Input, PageContainer, PageHeader, SubtabNav, Spinner, toast, Switch } from "@braedonsaunders/appkit-ui";
import { useEffect, useState } from "react";
import { Artwork } from "../components/Artwork";
import { api, apiFetch, errorMessage } from "../lib/api";
import type { EtherUser } from "../lib/types";
import type { TitleCard } from "../lib/contract";

type Settings = { enabled: boolean; plex: boolean; trakt: boolean; notify: boolean; prewarm: boolean; checkHours: number; naturalSearch: boolean; recaps: boolean };
type Ratings = { enabled: boolean; provider: string; apiKey: string; critic: string; audience: string; cacheHours: number };
type Overlays = { enabled: boolean; status: boolean; quality: boolean; ratings: boolean; personalize: boolean; localize: boolean };
type Variant = { path: string; language?: string; tags?: string[] };
type Art = { posters: Variant[] | null; backdrops: Variant[] | null; pinnedPoster?: string; pinnedBackdrop?: string };
type Item = { title: { key: string | number; title: string; released?: string; posterPath?: string }; sources: string[]; availableAt?: string; checkedAt?: string };
type Badge = { source: string; value: number; scale: number };
const panel = "space-y-4 rounded-xl border border-border bg-surface p-5";
const select = "block w-full rounded-md border border-border bg-surface px-3 py-2 text-sm";
const date = (value?: string) => value && !value.startsWith("0001") ? new Date(value).toLocaleDateString() : "Date not announced";

export default function Discovery() {
 const [tab, setTab] = useState("watchlist");const [ratingTab,setRatingTab]=useState("settings");const [artTab,setArtTab]=useState("appearance");
 const [savedSettings,setSavedSettings]=useState<Settings|null>(null);const [savedRatings,setSavedRatings]=useState<Ratings|null>(null);const [savedOverlays,setSavedOverlays]=useState<Overlays|null>(null);
 const [settings, setSettings] = useState<Settings | null>(null);
 const [ratings, setRatings] = useState<Ratings | null>(null);
 const [overlays, setOverlays] = useState<Overlays | null>(null);
 const [users, setUsers] = useState<EtherUser[]>([]);
 const [viewer, setViewer] = useState(0);
 const [items, setItems] = useState<Item[]>([]);
 const [busy, setBusy] = useState(false);
 const [loading, setLoading] = useState(true);
 const [query, setQuery] = useState("");
 const [results, setResults] = useState<TitleCard[]>([]);
 const [title, setTitle] = useState<TitleCard | null>(null);
 const [art, setArt] = useState<Art>({ posters: [], backdrops: [] });
 const [variants, setVariants] = useState<Art>({ posters: [], backdrops: [] });
 const [badges, setBadges] = useState<Badge[]>([]);
 const [lang, setLang] = useState("en");
 const [preview, setPreview] = useState(0);
 const perform = async (fn: () => Promise<unknown>) => { setBusy(true); try { await fn(); } catch (error) { toast.error(errorMessage(error)); } finally { setBusy(false); } };
 useEffect(() => { void Promise.all([
  apiFetch<Settings>("/discovery/settings").then(v=>{setSettings(v);setSavedSettings(v)}),
  apiFetch<Ratings>("/discovery/ratings/settings").then(v=>{setRatings(v);setSavedRatings(v)}),
  apiFetch<Overlays>("/discovery/overlays/settings").then(v=>{setOverlays(v);setSavedOverlays(v)}),
  api.users().then(value => { setUsers(value); setViewer(value[0]?.id ?? 0); }),
 ]).catch(error => toast.error(errorMessage(error))).finally(() => setLoading(false)); }, []);
 useEffect(() => { if (!viewer) return; const controller = new AbortController(); void apiFetch<Item[]>(`/discovery/users/${viewer}/`, { signal: controller.signal }).then(setItems).catch(error => { if (!controller.signal.aborted) toast.error(errorMessage(error)); }); return () => controller.abort(); }, [viewer]);
 useEffect(() => { if (!title) return; const controller = new AbortController(); void apiFetch<Art>(`/discovery/overlays/artwork/${title.key}`, { signal: controller.signal }).then(setArt).catch(error => { if (!controller.signal.aborted) toast.error(errorMessage(error)); }); return () => controller.abort(); }, [title]);
 const search = async () => { setResults(await api.searchCatalog(query, undefined, 12)); };
 const saveDiscovery = async () => { const next=await apiFetch<Settings>("/discovery/settings", { method: "PUT", body: JSON.stringify(settings) });setSettings(next);setSavedSettings(next); toast.success("Discovery settings saved"); };
 const pickTitle = (value: TitleCard) => { setTitle(value); setVariants({ posters: [], backdrops: [] }); setBadges([]); };
 const titlePicker = <section className={panel}>
    <h2 className="text-lg font-semibold">Choose a title</h2>
    <div className="flex gap-2"><Input aria-label="Find a catalogue title" value={query} placeholder="Search movies and shows" onChange={e => setQuery(e.target.value)} onKeyDown={e => { if (e.key === "Enter" && query.trim()) void perform(search); }} /><Button variant="outline" disabled={busy || !query.trim()} onClick={() => void perform(search)}>Search</Button></div>
    <div className="flex flex-wrap gap-2">{results.map(result => <Button key={result.key} variant={title?.key === result.key ? "default" : "outline"} onClick={() => pickTitle(result)}>{result.title}{result.year ? ` (${result.year})` : ""}</Button>)}</div>
    {title && <div className="flex flex-wrap items-center gap-3"><p className="font-medium">{title.title}</p>{tab === "watchlist" && <Button disabled={busy || !viewer} onClick={() => void perform(async () => { await apiFetch(`/discovery/users/${viewer}/items/${title.key}`, { method: "PUT", body: "{}" }); setItems(await apiFetch<Item[]>(`/discovery/users/${viewer}/`)); toast.success("Reminder saved"); })}>Remind this viewer</Button>}</div>}
   </section>;
 const choose = <K extends keyof Settings>(key: K, value: Settings[K]) => setSettings(s => s ? { ...s, [key]: value } : s);
 return <PageContainer className="space-y-6">
  <PageHeader title="Discovery" description="Watchlists, ratings and artwork in your viewers’ Plex apps." />
  <div className="border-b border-border"><SubtabNav ariaLabel="Discovery tools" active={tab} onSelect={key => setTab(key as typeof tab)} tabs={[{ key: "watchlist", label: "Watchlists" }, { key: "ratings", label: "Ratings" }, { key: "artwork", label: "Artwork" }, { key: "search", label: "Search & recaps" }]} /></div>
  {loading ? <Spinner label="Loading discovery settings" /> : <>
   {(tab === "watchlist" || tab === "artwork") && <label className="block text-sm">Viewer<select className={select} value={viewer} onChange={e => setViewer(Number(e.target.value))}>{users.map(user => <option key={user.id} value={user.id}>{user.title || user.username}</option>)}</select></label>}
   {tab === "watchlist" && settings && <section className={panel}>
    <h2 className="text-lg font-semibold">Watchlist → availability</h2>
    <p className="text-sm text-fg-muted">Coming Soon and Now available appear on each viewer’s Plex Home. An available title requires a healthy release; release dates alone do not trigger notifications.</p>
    <EditPanel onDiscard={()=>setSettings(savedSettings)} title="Watchlist monitoring" description="Connect authorized watchlists and choose when to check releases." action="Edit monitoring" summary={savedSettings?.enabled ? "Monitoring enabled" : "Monitoring is off"}>    {([ ["enabled", "Enable watchlist monitoring"], ["plex", "Sync Plex Watchlists using authorized account tokens"], ["trakt", "Sync connected Trakt Watchlists"], ["notify", "Send availability events to configured owner notification channels"], ["prewarm", "Warm opening segments when a release becomes available"] ] as const).map(([key, label]) => <label key={key} className="flex items-center gap-2 text-sm"><Switch type="checkbox" checked={settings[key]} onChange={e => choose(key, e.target.checked)} />{label}</label>)}
    <p className="text-xs text-fg-muted">Owner and managed Home accounts need valid Plex account authorization. A friend’s server access token does not grant access to their private watchlist. Friends can connect Trakt. Ether does not use a friend’s server token to access their Plex account. Mobile push through Plex is not enabled.</p>
    <label className="block text-sm">Minimum hours between checks<Input type="number" min={1} max={168} value={settings.checkHours} onChange={e => choose("checkHours", Number(e.target.value))} /></label>
    <div className="flex flex-wrap gap-2"><Button disabled={busy} onClick={() => void perform(saveDiscovery)}>Save settings</Button></div>
</EditPanel><div className="flex flex-wrap gap-2"><Button variant="outline" disabled={busy || !viewer || !settings.enabled} onClick={() => void perform(async () => setItems(await apiFetch<Item[]>(`/discovery/users/${viewer}/sync`, { method: "POST", body: "{}" })))}>Sync viewer</Button><Button variant="outline" disabled={busy || !viewer || !settings.enabled} onClick={() => void perform(async () => setItems(await apiFetch<Item[]>(`/discovery/users/${viewer}/check`, { method: "POST", body: "{}" })))}>Check availability</Button></div>
    {items.length === 0 ? <p className="text-sm text-fg-muted">No watched-for titles for this viewer.</p> : items.map(item => <div key={String(item.title.key)} className="flex items-center justify-between gap-3 border-t border-border pt-3"><div><p>{item.title.title}</p><p className="text-xs text-fg-muted">{item.availableAt && !item.availableAt.startsWith("0001") ? `Now available · ${date(item.availableAt)}` : `Coming Soon · ${date(item.title.released)}`} · {item.sources.join(", ")}</p></div><Button size="sm" variant="outline" disabled={busy} onClick={() => void perform(async () => { await apiFetch(`/discovery/users/${viewer}/items/${item.title.key}`, { method: "DELETE" }); setItems(value => value.filter(v => v.title.key !== item.title.key)); })}>Remove reminder</Button></div>)}
   </section>}
   {tab === "watchlist" && <EditPanel title="Add a reminder" description="Choose a movie or show to watch for." action="Add reminder">{titlePicker}</EditPanel>}
   {tab === "ratings" && ratings && <EditPanel onDiscard={()=>setRatings(savedRatings)} title="Rating sources" description="Connect your OMDb or MDBList account and choose the badges shown in Plex." action="Edit rating sources" summary={savedRatings?.enabled ? `Enabled · ${savedRatings.provider}` : "Catalogue ratings only"}><SubtabNav ariaLabel="Rating tools" active={ratingTab} onSelect={setRatingTab} tabs={[{key:"settings",label:"Sources"},{key:"preview",label:"Title preview"}]} />{ratingTab==="settings" && <section className={panel}>
    <label className="flex items-center gap-2 text-sm"><Switch type="checkbox" checked={ratings.enabled} onChange={e => setRatings({ ...ratings, enabled: e.target.checked })} />Enable external ratings</label>
    <label className="block text-sm">Provider<select className={select} value={ratings.provider} onChange={e => setRatings({ ...ratings, provider: e.target.value })}><option value="omdb">OMDb</option><option value="mdblist">MDBList</option></select></label>
    <label className="block text-sm">Owner API key<Input type="password" autoComplete="new-password" value={ratings.apiKey} onChange={e => setRatings({ ...ratings, apiKey: e.target.value })} /></label>
    <p className="text-xs text-fg-muted">OMDb supplies IMDb, Rotten Tomatoes critics and Metacritic. MDBList can additionally supply audience and Letterboxd ratings. Missing scores stay absent. Posters retain each source’s original scale.</p>
    {([ ["critic", "Plex rating"], ["audience", "Plex audience rating"] ] as const).map(([key, label]) => <label key={key} className="block text-sm">{label}<select className={select} value={ratings[key]} onChange={e => setRatings({ ...ratings, [key]: e.target.value })}><option value="">Keep catalogue default</option><option value="imdb">IMDb</option><option value="tomatoes">Rotten Tomatoes critics</option><option value="popcorn">Rotten Tomatoes audience (MDBList)</option></select></label>)}
    <label className="block text-sm">Cache for hours<Input type="number" min={1} max={2160} value={ratings.cacheHours} onChange={e => setRatings({ ...ratings, cacheHours: Number(e.target.value) })} /></label>
    <div className="flex gap-2"><Button disabled={busy} onClick={() => void perform(async () => { const next=await apiFetch<Ratings>("/discovery/ratings/settings", { method: "PUT", body: JSON.stringify(ratings) });setRatings(next);setSavedRatings(next); toast.success("Ratings settings saved"); })}>Save ratings</Button></div>
</section>}{ratingTab==="preview" && <section className="space-y-4">{titlePicker}<Button variant="outline" disabled={busy || !title || !ratings.enabled} onClick={() => void perform(async () => { setBadges(await apiFetch<Badge[]>(`/discovery/ratings/refresh/${title!.key}`, { method: "POST", body: "{}" })); })}>Refresh selected title</Button>
    {badges.map(badge => <p className="text-sm" key={badge.source}>{badge.source}: {badge.value}/{badge.scale}</p>)}
   </section>}</EditPanel>}
   {tab === "artwork" && overlays && <EditPanel onDiscard={()=>setOverlays(savedOverlays)} title="Artwork preferences" description="Choose overlays, localization and personalization." action="Edit artwork preferences"><SubtabNav ariaLabel="Artwork tools" active={artTab} onSelect={setArtTab} tabs={[{key:"appearance",label:"Appearance"},{key:"title",label:"Title artwork"}]} />{artTab==="appearance" && <section className={panel}>
    <div className="grid gap-6 md:grid-cols-[1fr_200px]"><div className="space-y-4">{([ ["enabled", "Enable poster variants"], ["status", "Show observed release and season status"], ["quality", "Show verified 4K / HDR / DV / Atmos availability"], ["ratings", "Show cached ratings with source labels"], ["personalize", "Choose tagged variants using viewer taste"], ["localize", "Use each Plex viewer’s requested metadata language"] ] as const).map(([key, label]) => <label key={key} className="flex items-center gap-2 text-sm"><Switch type="checkbox" checked={overlays[key]} onChange={e => setOverlays({ ...overlays, [key]: e.target.checked })} />{label}</label>)}<p className="text-xs text-fg-muted">Status and quality badges only appear when there is evidence. The preview uses the selected viewer and your unsaved display settings.</p><label className="block text-sm">Preview language<Input value={lang} onChange={e => setLang(e.target.value)} placeholder="en, fr-CA, es" /></label><Button disabled={busy} onClick={() => void perform(async () => { const next=await apiFetch<Overlays>("/discovery/overlays/settings", { method: "PUT", body: JSON.stringify(overlays) });setOverlays(next);setSavedOverlays(next); setPreview(v => v + 1); toast.success("Artwork settings saved"); })}>Save artwork settings</Button></div>{title && <Artwork src={`/ether/api/discovery/overlays/preview/${title.key}?userId=${viewer}&lang=${encodeURIComponent(lang)}&v=${preview}&settings=${encodeURIComponent(JSON.stringify(overlays))}`} alt={`${title.title} poster preview`} className="aspect-[2/3] w-[200px] rounded-lg" eager />}</div>
</section>}{artTab==="title" && <section className="space-y-4">{titlePicker}
    {title && <><Button variant="outline" disabled={busy} onClick={() => void perform(async () => { const loaded = await apiFetch<Art>(`/discovery/overlays/variants/${title.key}?lang=${encodeURIComponent(lang)}`); setVariants(loaded); setArt(current => ({ ...current, posters: (loaded.posters ?? []).map(v => ({ ...v, tags: current.posters?.find(p => p.path === v.path)?.tags ?? [] })), backdrops: loaded.backdrops })); })}>Load TMDB artwork choices</Button>
    <p className="text-xs text-fg-muted">For automatic choices, tag posters with the genres or people they emphasize. These tags match the selected viewer’s learned taste. A pinned poster overrides automatic choices for this title.</p><div className="grid grid-cols-3 gap-3 sm:grid-cols-5">{variants.posters?.map(variant => <div key={variant.path} className="space-y-2"><button type="button" className={`rounded-lg border-2 ${art.pinnedPoster === variant.path ? "border-accent" : "border-transparent"}`} onClick={() => setArt({ ...art, pinnedPoster: variant.path })}><Artwork src={`https://image.tmdb.org/t/p/w185${variant.path}`} alt={`Poster (${variant.language || "textless"})`} className="aspect-[2/3]" /></button><ListInput ariaLabel={`Focus tags for ${variant.path}`} placeholder="Drama, actor name" value={art.posters?.find(p=>p.path===variant.path)?.tags??[]} onChange={tags=>setArt(current=>({...current,posters:current.posters?.map(p=>p.path===variant.path?{...p,tags:tags.map(String)}:p)??[]}))}/></div>)}</div>
    <label className="block text-sm">Pinned backdrop<select className={select} value={art.pinnedBackdrop || ""} onChange={e => setArt({ ...art, pinnedBackdrop: e.target.value })}><option value="">Automatic textless backdrop</option>{variants.backdrops?.map(v => <option key={v.path} value={v.path}>{v.language || "Textless"} · {v.path}</option>)}</select></label>
    <div className="flex flex-wrap gap-2"><Button disabled={busy} onClick={() => void perform(async () => { await apiFetch(`/discovery/overlays/artwork/${title.key}`, { method: "PUT", body: JSON.stringify(art) }); setPreview(v => v + 1); toast.success("Artwork choice saved"); })}>Save title artwork</Button><Button variant="outline" onClick={() => setArt({ ...art, pinnedPoster: "", pinnedBackdrop: "" })}>Use automatic choices</Button></div></>}
   </section>}</EditPanel>}
   {tab === "search" && settings && <EditPanel onDiscard={()=>setSettings(savedSettings)} title="Search & recaps" description="Optional features powered by your connected language model." action="Configure search & recaps"><section className={panel}>
    <h2 className="text-lg font-semibold">Optional language model</h2><p className="text-sm text-fg-muted">Configure the owner’s LLM endpoint in Settings. These features use that endpoint. Search falls back to ordinary catalogue search when it is unavailable.</p>
    <label className="flex items-center gap-2 text-sm"><Switch type="checkbox" checked={settings.naturalSearch} onChange={e => choose("naturalSearch", e.target.checked)} />Natural-language search in Plex, such as “90s heist movies”</label>
    <label className="flex items-center gap-2 text-sm"><Switch type="checkbox" checked={settings.recaps} onChange={e => choose("recaps", e.target.checked)} />Recaps based only on episodes this viewer has completed</label>
    <p className="text-xs text-fg-muted">Recaps send completed episode overviews to the configured LLM. Year in Review and Rediscover use local watch history. Removing Continue Watching in Plex stays dismissed across later episodes.</p>
    <Button disabled={busy} onClick={() => void perform(saveDiscovery)}>Save search settings</Button>
   </section></EditPanel>}
  </>}
 </PageContainer>;
}
