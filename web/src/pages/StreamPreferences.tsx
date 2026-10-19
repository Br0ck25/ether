import { EditPanel } from "../components/EditPanel";
import { Button, PageContainer, PageHeader, SubtabNav, Input, Switch, Textarea } from "@braedonsaunders/appkit-ui";
import { useCallback, useEffect, useState } from "react";
import { api, apiFetch, errorMessage } from "../lib/api";
import { TitleSearch } from "../components/pickers";
import type { EtherUser } from "../lib/types";

type Rules = { audioLanguages: string[]; subtitleLanguages: string[]; preferOriginal: boolean; subtitleMode: string; subtitleStyle: string; loudness: boolean; nightMode: boolean; dialogueBoost: boolean };
type Memory = { showId: number; showTitle?:string; kind: string; choice: { language: string; forced: boolean; sdh: boolean; off: boolean } };
type Coverage = { showId: number; cachedEpisodes: number; introEpisodes: number; creditsEpisodes: number; recapEpisodes: number };
const initial: Rules = { audioLanguages: [], subtitleLanguages: [], preferOriginal: false, subtitleMode: "off", subtitleStyle: "normal", loudness: false, nightMode: false, dialogueBoost: false };
const inputClass = "rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg";
const languages = (s: string) => s.split(",").map(x => x.trim().toLowerCase()).filter(Boolean);

export default function StreamPreferences() {
  const [preferencesOpen,setPreferencesOpen]=useState(false);const [syncOpen,setSyncOpen]=useState(false);const [savedRules,setSavedRules]=useState<Rules>(initial);
  const [tab, setTab] = useState("preferences");
  const [users, setUsers] = useState<EtherUser[]>([]);
  const [user, setUser] = useState(0);
  const [rules, setRules] = useState<Rules>(initial);
  const [audioText, setAudioText] = useState("");
  const [subtitleText, setSubtitleText] = useState("");
  const [memory, setMemory] = useState<Memory[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [show, setShow] = useState("");
  const [coverage, setCoverage] = useState<Coverage | null>(null);
  const [subtitle, setSubtitle] = useState("");
  const [offset, setOffset] = useState("0");
  const [rate, setRate] = useState("1");
  const [reference, setReference] = useState("");
  const [applySync, setApplySync] = useState(false);
  const [synced, setSynced] = useState("");
  const [cached,setCached]=useState<{id:string;language:string;release:string}[]>([]);
  const [showName,setShowName]=useState("");
  const applyRules = useCallback((r: Rules) => {
    const normalized = {...initial,...r,audioLanguages:r.audioLanguages??[],subtitleLanguages:r.subtitleLanguages??[]};
    setRules(normalized);setSavedRules(normalized);
    setAudioText(normalized.audioLanguages.join(", "));
    setSubtitleText(normalized.subtitleLanguages.join(", "));
  }, []);
  useEffect(()=>{void apiFetch<{id:string;language:string;release:string}[]>("/subtitle-sync/").then(setCached).catch(()=>setCached([]))},[]);
  useEffect(() => { let active = true; void api.users().then(v => { if (active) setUsers(v); }).catch(e => { if (active) setMessage(errorMessage(e)); }); return () => { active = false; }; }, []);
  useEffect(() => { const controller = new AbortController(); setLoaded(false); setMessage(""); void Promise.all([apiFetch<Rules>(`/stream-preferences/${user}`, { signal: controller.signal }), apiFetch<Memory[]>(`/stream-preferences/${user}/shows`, { signal: controller.signal })]).then(([r, m]) => { if (controller.signal.aborted) return; applyRules(r); setMemory(m??[]); setLoaded(true); }).catch(e => { if (!controller.signal.aborted) setMessage(errorMessage(e)); }); return () => controller.abort(); }, [user, applyRules]);
  async function save() { setBusy(true); setMessage(""); try { const next={...rules,audioLanguages:languages(audioText),subtitleLanguages:languages(subtitleText)};await apiFetch(`/stream-preferences/${user}`, { method: "PUT", body: JSON.stringify(next) });applyRules(next); setMessage("Stream preferences saved."); } catch (e) { setMessage(errorMessage(e)); } finally { setBusy(false); } }
  async function reset() { setBusy(true); try { await apiFetch(`/stream-preferences/${user}`, { method: "DELETE" }); applyRules(await apiFetch<Rules>(`/stream-preferences/${user}`)); setMessage("Owner defaults restored."); } catch (e) { setMessage(errorMessage(e)); } finally { setBusy(false); } }
  async function forget(showId: number) { try { await apiFetch(`/stream-preferences/${user}/shows/${showId}`, { method: "DELETE" }); setMemory(m => m.filter(x => x.showId !== showId)); } catch (e) { setMessage(errorMessage(e)); } }
  async function loadCoverage() { setCoverage(null); try { setCoverage(await apiFetch<Coverage>(`/markers/shows/${encodeURIComponent(show)}`)); } catch (e) { setMessage(errorMessage(e)); } }
  async function sync() { setBusy(true); setSynced(""); try { const result = await apiFetch<{ externalUrl: string }>(`/subtitle-sync/${encodeURIComponent(subtitle.replace(/^ether-subtitle:/, ""))}`, { method: "POST", body: JSON.stringify({ offsetMs: Number(offset), rate: Number(rate), reference, apply: applySync }) }); setSynced(result.externalUrl); } catch (e) { setMessage(errorMessage(e)); } finally { setBusy(false); } }
  return <PageContainer className="max-w-4xl space-y-8">
    <PageHeader title="Audio, subtitles and skipping" description="Set owner defaults or a viewer override. Plex track changes are remembered for later episodes of the same show." />
    <div className="border-b border-border"><SubtabNav ariaLabel="Stream tools" active={tab} onSelect={setTab} tabs={[{key:"preferences",label:"Preferences"},{key:"memory",label:"Show memory"},{key:"markers",label:"Intro & credits"},{key:"sync",label:"Subtitle timing"}]} /></div>
    {message && !preferencesOpen && !syncOpen && <p role="status" className="rounded-md border border-border p-3 text-sm">{message}</p>}
    <label className="flex flex-col gap-2 text-sm">Viewer<select className={inputClass} value={user} onChange={e => setUser(Number(e.target.value))}><option value={0}>Owner defaults for all viewers</option>{users.map(u => <option key={u.id} value={u.id}>{u.title || u.username}</option>)}</select></label>
    {tab === "preferences" && <EditPanel onOpen={()=>setMessage("")} onOpenChange={setPreferencesOpen} onDiscard={()=>applyRules(savedRules)} title="Audio & subtitle preferences" description="Defaults work automatically. Override them for one viewer when needed." action="Edit preferences" summary={`${savedRules.audioLanguages.join(", ") || "Any audio language"} · subtitles ${savedRules.subtitleMode}`} ><fieldset disabled={!loaded || busy} className="space-y-4 rounded-lg border border-border p-5">
      <legend className="px-2 font-medium">Language and audio policy</legend>{message&&<p role="status" className="text-sm">{message}</p>}
      <label className="flex flex-col gap-2 text-sm">Audio languages, in preference order<Input className={inputClass} value={audioText} onChange={e => setAudioText(e.target.value)} placeholder="en, ja, fr" /></label>
      <label className="flex items-center gap-2 text-sm"><Switch type="checkbox" checked={rules.preferOriginal} onChange={e => setRules({ ...rules, preferOriginal: e.target.checked })} />Prefer the title’s original language before dubs</label>
      <label className="flex flex-col gap-2 text-sm">Subtitle languages, in preference order<Input className={inputClass} value={subtitleText} onChange={e => setSubtitleText(e.target.value)} placeholder="en, fr" /></label>
      <div className="grid gap-4 sm:grid-cols-2"><label className="flex flex-col gap-2 text-sm">Subtitle mode<select className={inputClass} value={rules.subtitleMode} onChange={e => setRules({ ...rules, subtitleMode: e.target.value })}><option value="off">Off</option><option value="forced-only">Forced tracks only</option><option value="foreign-only">When audio is foreign to subtitle languages</option><option value="always">Always</option></select></label><label className="flex flex-col gap-2 text-sm">Preferred subtitle style<select className={inputClass} value={rules.subtitleStyle} onChange={e => setRules({ ...rules, subtitleStyle: e.target.value })}><option value="normal">Normal</option><option value="sdh">SDH / hearing impaired</option><option value="forced">Forced</option></select></label></div>
      {([['loudness', 'Normalize loudness'], ['nightMode', 'Night mode compression'], ['dialogueBoost', 'Boost centre-channel dialogue']] as const).map(([key, label]) => <label key={key} className="flex items-center gap-2 text-sm"><Switch type="checkbox" checked={rules[key]} onChange={e => setRules({ ...rules, [key]: e.target.checked })} />{label}</label>)}
      <p className="text-xs text-fg-muted">Audio processing requires server audio transcoding. Dialogue boost applies to sources with six or more channels.</p>
      <div className="flex gap-2"><Button onClick={() => void save()}>Save preferences</Button>{user > 0 && <Button variant="outline" onClick={() => void reset()}>Use owner defaults</Button>}</div>
    </fieldset></EditPanel>}
    {tab === "memory" && <section className="space-y-3 rounded-lg border border-border p-5"><h2 className="font-medium">Remembered show choices</h2>{memory.length === 0 ? <p className="text-sm text-fg-muted">No show-specific choices saved for this viewer.</p> : memory.map(m => <div key={`${m.showId}-${m.kind}`} className="flex items-center justify-between gap-3 text-sm"><span>{m.showTitle || "Unavailable show"} · {m.kind}: {m.choice.off ? "off" : `${m.choice.language || "unknown language"}${m.choice.forced ? " · forced" : ""}${m.choice.sdh ? " · SDH" : ""}`}</span><Button size="sm" variant="outline" onClick={() => void forget(m.showId)}>Forget show</Button></div>)}</section>}
    {tab === "markers" && <section className="space-y-3 rounded-lg border border-border p-5"><h2 className="font-medium">Marker coverage</h2><p className="text-sm text-fg-muted">Counts include episodes observed in the local marker cache. Skip controls depend on the Plex app.</p><TitleSearch type="show" placeholder="Find a show for marker coverage…" onPick={title=>{setShow(String(Number(title.key)%1000000000000000));setShowName(title.title)}}/>{showName&&<p className="text-sm">{showName}</p>}<Button variant="outline" disabled={!/^\d+$/.test(show)} onClick={() => void loadCoverage()}>Check coverage</Button>{coverage && <p className="text-sm">{coverage.cachedEpisodes} cached episodes · {coverage.introEpisodes} intros · {coverage.creditsEpisodes} credits · {coverage.recapEpisodes} recaps</p>}</section>}
    {tab === "sync" && <EditPanel onOpen={()=>setMessage("")} onOpenChange={setSyncOpen} title="Subtitle timing" description="Create a corrected copy of a downloaded subtitle. Original bytes are preserved." action="Adjust subtitle timing"><section className="space-y-3 rounded-lg border border-border p-5"><h2 className="font-medium">Subtitle sync tool</h2>{message&&<p role="status" className="text-sm">{message}</p>}<p className="text-sm text-fg-muted">Create an adjusted copy of an existing cached sidecar. Positive offsets display subtitles later. Supplying reference subtitle text estimates a constant offset from matching dialogue.</p><label className="flex flex-col gap-2 text-sm">Subtitle<select className={inputClass} value={subtitle} onChange={e=>setSubtitle(e.target.value)}><option value="">Choose a downloaded subtitle</option>{cached.map(c=><option key={c.id} value={c.id}>{c.release||"Downloaded subtitle"} · {c.language||"Unknown language"}</option>)}</select></label>{cached.length===0&&<p className="text-sm text-fg-muted">Downloaded subtitles appear here after playback finds a matching sidecar.</p>}<div className="grid gap-4 sm:grid-cols-2"><label className="flex flex-col gap-2 text-sm">Offset, milliseconds<Input className={inputClass} type="number" min={-3600000} max={3600000} value={offset} onChange={e => setOffset(e.target.value)} /></label><label className="flex flex-col gap-2 text-sm">Timing multiplier<Input className={inputClass} type="number" min={0.5} max={2} step={0.001} value={rate} onChange={e => setRate(e.target.value)} /></label></div><label className="flex flex-col gap-2 text-sm">Optional embedded reference subtitles<Textarea className={inputClass} rows={5} value={reference} onChange={e => setReference(e.target.value)} /></label><label className="flex items-center gap-2 text-sm"><Switch type="checkbox" checked={applySync} onChange={e => setApplySync(e.target.checked)} />Apply correction to this cached sidecar for all viewers</label><Button variant="outline" disabled={busy || !subtitle || !Number.isFinite(Number(offset)) || !Number.isFinite(Number(rate))} onClick={() => void sync()}>Create synced copy</Button>{synced && <p role="status" className="break-all text-sm">Synced sidecar: <code>{synced}</code></p>}</section></EditPanel>}
  </PageContainer>;
}
