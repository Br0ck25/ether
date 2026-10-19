import { Button, PageHeader } from "@braedonsaunders/appkit-ui";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, apiFetch } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { ProwlarrImport } from "../components/ProwlarrImport";
import { ErrorPanel, LoadingRows } from "../components/feedback";

interface Policy { family: string; dolbyVisionProfiles: number[] | null; subtitleCodecs: string[]; notes: string }
interface ResolverPolicy extends Record<string, unknown> { optimisticStart?: boolean; healthRace?: number; customFormats?: unknown[]; customFormatScores?: Record<string, number> }

export default function PlaybackPolicy() {
  const cfg = useAsync(() => api.getConfig(), []);
  const matrix = useAsync(() => apiFetch<{ policies: Policy[] }>("/playback/policy"), []);
  const [draft, setDraft] = useState<ResolverPolicy | null>(null);
  const [formats, setFormats] = useState("[]");
  const [scores, setScores] = useState("{}");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);
  useEffect(() => { if (cfg.data) { const resolver = cfg.data.resolver as ResolverPolicy; setDraft({ ...resolver }); setFormats(JSON.stringify(resolver.customFormats ?? [], null, 2)); setScores(JSON.stringify(resolver.customFormatScores ?? {}, null, 2)); } }, [cfg.data]);
  async function save() {
    if (!draft) return;
    setBusy(true); setError(null); setSaved(false);
    try {
      const customFormats: unknown = JSON.parse(formats);
      const customFormatScores: unknown = JSON.parse(scores);
      if (!Array.isArray(customFormats)) throw new Error("Custom formats must be a JSON array.");
      if (customFormatScores === null || Array.isArray(customFormatScores) || typeof customFormatScores !== "object" || Object.values(customFormatScores).some((v) => typeof v !== "number" || !Number.isInteger(v))) throw new Error("Scores must map format IDs to integer scores.");
      await api.putSection("resolver", { ...draft, customFormats, customFormatScores });
      await cfg.reload(); setSaved(true);
    } catch (e) { setError(e); } finally { setBusy(false); }
  }
  return <div className="space-y-6 p-6">
    <PageHeader title="Playback" description="Choose how quickly playback starts and inspect the rules that keep video in its original quality." />
    {cfg.error ? <ErrorPanel error={cfg.error} /> : !draft ? <LoadingRows /> : <section className="space-y-4 rounded-lg border border-border p-5">
      <label className="flex items-center gap-3"><input type="checkbox" checked={draft.optimisticStart ?? false} onChange={(e) => setDraft({ ...draft, optimisticStart: e.target.checked })} />Optimistic start</label>
      <p className="text-sm text-fg-muted">Begin the highest ranked release while its health check runs. A failed check marks the release bad; missing articles during playback trigger release failover.</p>
      <label className="flex items-center gap-3">Concurrent health race<select className="rounded border border-border bg-surface p-2" value={draft.healthRace ?? 0} onChange={(e) => setDraft({ ...draft, healthRace: Number(e.target.value) })}><option value={0}>Ranked selection</option><option value={2}>First healthy of 2</option><option value={3}>First healthy of 3</option><option value={4}>First healthy of 4</option></select></label>
      <p className="text-sm text-fg-muted">Racing may choose a lower ranked release sooner and uses more NZB grabs. Detail-page warming has its own small request and connection budget.</p>
      <details><summary className="cursor-pointer font-medium">Release scoring · TRaSH custom formats</summary><div className="mt-4 space-y-3"><p className="text-sm text-fg-muted">Paste native custom-format definitions as an array. Scores override defaults by trash_id. Unknown rules are rejected during import; review the resolver traces after changing scores.</p><label className="block">Definitions<textarea aria-label="Custom format definitions" className="mt-1 block min-h-64 w-full rounded border border-border bg-surface p-3 font-mono text-xs" value={formats} onChange={(e) => setFormats(e.target.value)} /></label><label className="block">Score overrides<textarea aria-label="Custom format score overrides" className="mt-1 block min-h-24 w-full rounded border border-border bg-surface p-3 font-mono text-xs" value={scores} onChange={(e) => setScores(e.target.value)} /></label><Link to="/traces" className="underline">Inspect release scores and rejection reasons</Link></div></details>
      {error ? <ErrorPanel error={error} /> : null}<Button disabled={busy} onClick={() => void save()}>{busy ? "Saving…" : "Save playback settings"}</Button>{saved ? <span role="status" className="ml-3 text-sm">Saved</span> : null}
    </section>}
    <ProwlarrImport />
    <section className="space-y-3"><h2 className="text-lg font-medium">Client rules</h2><p className="text-sm text-fg-muted">Actual client profiles refine these defaults. Model, display and audio receiver support must be declared by the client. Text subtitles stay separate; supported image subtitles keep video copy.</p>{matrix.error ? <ErrorPanel error={matrix.error} /> : !matrix.data ? <LoadingRows /> : <div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr><th className="p-3">Client</th><th className="p-3">DV profiles</th><th className="p-3">Subtitle rendering</th><th className="p-3">Decision notes</th></tr></thead><tbody>{matrix.data.policies.map((p) => <tr className="border-t border-border" key={p.family}><td className="p-3 font-medium">{p.family}</td><td className="p-3">{p.dolbyVisionProfiles?.join(", ") || "Client declaration"}</td><td className="p-3">{p.subtitleCodecs.join(", ")}</td><td className="p-3 text-fg-muted">{p.notes}</td></tr>)}</tbody></table></div>}</section>
  </div>;
}
