import { DashboardPanel } from "@braedonsaunders/appkit-dashboard/primitives";
import { Avatar, Badge, Button, EmptyState, Slider, toast, cn } from "@braedonsaunders/appkit-ui";
import { Brain, CalendarRange, Clapperboard, Clock3, Languages, Plus, Save, Sparkles, Tags, Undo2, UserRound, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Columns, DivergingBars, HourStrip, Meter } from "../../components/charts";
import { Segmented } from "../../components/fields";
import { FacetSelect, LookupPicker } from "../../components/pickers";
import { api, errorMessage } from "../../lib/api";
import type { OverrideKind, TasteOverride, TasteProfile } from "../../lib/contract";
import { formatMinutes, relativeTime } from "../../lib/format";

const DEVICE_NAMES: Record<string, string> = { tv: "TV", mobile: "Phone or tablet", web: "Web browser", desktop: "Desktop app" };

const langName = (code: string) => {
  try { return new Intl.DisplayNames(undefined, { type: "language" }).of(code) ?? code; } catch { return code; }
};

function confidenceText(c: number): string {
  if (c < 0.25) return "Still learning: only a few plays so far. Recommendations lean on popularity.";
  if (c < 0.6) return "Getting to know them: rows are personal but still exploring.";
  return "Confident: rows are tuned closely to their history.";
}

/**
 * Taste profile visualisation plus owner corrections. Override values:
 * genre = genre name, keyword = keyword name, person = TMDB person id.
 */
export function TasteView({ userId, taste, onChange }: { userId: number; taste: TasteProfile; onChange: (t: TasteProfile) => void }) {
  const [overrides, setOverrides] = useState<TasteOverride[]>(taste.overrides ?? []);
  const [names, setNames] = useState<Record<string, string>>({});
  const [selectedGenre, setSelectedGenre] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  useEffect(() => setOverrides(taste.overrides ?? []), [taste]);
  const dirty = JSON.stringify(overrides) !== JSON.stringify(taste.overrides ?? []);

  const personName = (id: string) => names[id] ?? taste.people.find((p) => String(p.id) === id)?.name ?? `Person ${id}`;
  const setOverride = (kind: OverrideKind, value: string, weight: number | null) => {
    setOverrides((list) => {
      const rest = list.filter((o) => !(o.kind === kind && o.value === value));
      return weight === null ? rest : [...rest, { kind, value, weight: Math.round(weight * 100) / 100 }];
    });
  };
  const overrideFor = (kind: OverrideKind, value: string) => overrides.find((o) => o.kind === kind && o.value === value);

  const save = async () => {
    setSaving(true);
    try {
      const next = await api.putOverrides(userId, overrides);
      onChange(next);
      toast.success("Taste corrected", { description: "Their rows are rebuilt with your changes." });
    } catch (e) { toast.error("Couldn't save", { description: errorMessage(e) }); }
    finally { setSaving(false); }
  };

  const genres = useMemo(() => [...taste.genres].sort((a, b) => b.weight - a.weight), [taste.genres]);
  const genreData = genres.map((g) => {
    const o = overrideFor("genre", g.name);
    return { key: g.name, label: g.name, weight: o ? o.weight : g.weight, hint: o ? `Owner override (learned ${g.weight.toFixed(2)})` : "Learned from viewing history" };
  });
  const decades = [...taste.decades].sort((a, b) => a.decade - b.decade);
  const sel = selectedGenre ? genres.find((g) => g.name === selectedGenre) : undefined;
  const selOverride = selectedGenre ? overrideFor("genre", selectedGenre) : undefined;

  return (
    <div className="space-y-6">
      <div className="grid gap-6 lg:grid-cols-3">
        <DashboardPanel title="What Ether has learned" icon={<Brain size={16} />} className="lg:col-span-1">
          <Meter value={taste.confidence} label="Confidence" valueLabel={`${Math.round(taste.confidence * 100)}%`} />
          <p className="mt-3 text-sm text-fg-muted">{confidenceText(taste.confidence)}</p>
          <p className="mt-3 text-xs text-fg-subtle">Updated {relativeTime(taste.updatedAt)}</p>
        </DashboardPanel>
        <DashboardPanel title="Habits" icon={<Clock3 size={16} />} className="lg:col-span-2">
          <div className="grid gap-4 sm:grid-cols-2">
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
              <dt className="text-fg-muted">Weeknights</dt><dd className="tabular-nums text-fg">{formatMinutes(taste.habits.weeknightMinutes)} a night</dd>
              <dt className="text-fg-muted">Weekends</dt><dd className="tabular-nums text-fg">{formatMinutes(taste.habits.weekendMinutes)} a day</dd>
              <dt className="text-fg-muted">Usually on</dt><dd className="text-fg">{DEVICE_NAMES[taste.habits.preferredDevice] ?? (taste.habits.preferredDevice || "—")}</dd>
            </dl>
            <div className="space-y-2.5">
              <Meter value={taste.habits.bingeScore} label="Binge watching" />
              <Meter value={taste.habits.rewatchScore} label="Rewatching" />
            </div>
          </div>
          <div className="mt-4"><p className="mb-1.5 text-xs text-fg-muted">When they watch</p><HourStrip peaks={taste.habits.peakHours ?? []} ariaLabel="Peak viewing hours" /></div>
        </DashboardPanel>
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <DashboardPanel title="Genres" icon={<Clapperboard size={16} />}
          actions={<span className="text-xs text-fg-subtle">Click a genre to correct it</span>}>
          {genreData.length ? (
            <DivergingBars data={genreData} ariaLabel="Genre affinity from dislike to like" onSelect={(d) => setSelectedGenre(d.key === selectedGenre ? null : d.key)} selectedKey={selectedGenre ?? undefined} />
          ) : <p className="text-sm text-fg-muted">No genre signal yet.</p>}
          {sel ? (
            <div className="mt-4 rounded-lg border border-border bg-bg-subtle p-3" aria-live="polite">
              <div className="flex items-center justify-between gap-2">
                <p className="text-sm font-medium text-fg">{sel.name}</p>
                <span className="text-xs text-fg-muted">Learned {sel.weight > 0 ? "+" : ""}{sel.weight.toFixed(2)}</span>
              </div>
              <div className="mt-2 flex flex-wrap gap-1.5">
                <Button size="sm" variant={selOverride?.weight === -1 ? "destructive" : "outline"} onClick={() => setOverride("genre", sel.name, -1)}>Not into it</Button>
                <Button size="sm" variant={!selOverride ? "subtle" : "outline"} onClick={() => setOverride("genre", sel.name, null)}>Use what Ether learned</Button>
                <Button size="sm" variant={selOverride?.weight === 1 ? "default" : "outline"} onClick={() => setOverride("genre", sel.name, 1)}>Loves it</Button>
              </div>
              <label className="mt-3 flex items-center gap-3 text-xs text-fg-muted">
                Fine-tune
                <Slider min={-1} max={1} step={0.05} value={selOverride?.weight ?? sel.weight} aria-label={`${sel.name} weight`}
                  onChange={(e) => setOverride("genre", sel.name, e.currentTarget.valueAsNumber)} className="flex-1" />
                <span className="w-10 text-right tabular-nums text-fg">{(selOverride?.weight ?? sel.weight).toFixed(2)}</span>
              </label>
            </div>
          ) : null}
        </DashboardPanel>

        <div className="space-y-6">
          <DashboardPanel title="People they follow" icon={<UserRound size={16} />}>
            {taste.people.length === 0 ? <p className="text-sm text-fg-muted">No favourite people yet.</p> : (
              <ul className="space-y-2">
                {[...taste.people].sort((a, b) => b.weight - a.weight).slice(0, 8).map((p) => (
                  <li key={p.id} className="flex items-center gap-3">
                    <Avatar src={p.image} name={p.name} size={30} />
                    <span className="min-w-0 flex-1"><span className="block truncate text-sm text-fg">{p.name}</span><span className="block text-xs text-fg-muted">{p.role}</span></span>
                    <span className="w-24"><Meter value={Math.max(0, p.weight)} /></span>
                  </li>
                ))}
              </ul>
            )}
          </DashboardPanel>
          <DashboardPanel title="Eras" icon={<CalendarRange size={16} />}>
            {decades.length ? <Columns ariaLabel="Affinity by decade" data={decades.map((d) => ({ key: String(d.decade), label: `’${String(d.decade).slice(2)}s`, value: d.weight, hint: `${d.decade}s` }))} /> : <p className="text-sm text-fg-muted">No era preference yet.</p>}
          </DashboardPanel>
        </div>
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <DashboardPanel title="Themes" icon={<Tags size={16} />}>
          {taste.keywords.length === 0 ? <p className="text-sm text-fg-muted">No themes yet.</p> : (
            <div className="flex flex-wrap gap-1.5">
              {[...taste.keywords].sort((a, b) => b.weight - a.weight).map((k) => (
                <span key={k.name} title={`Weight ${k.weight.toFixed(2)}`}
                  className={cn("rounded-full border px-2.5 py-0.5", k.weight >= 0 ? "border-primary/30 bg-primary-subtle text-primary" : "border-border text-fg-subtle line-through",
                    Math.abs(k.weight) > 0.6 ? "text-sm font-semibold" : Math.abs(k.weight) > 0.3 ? "text-sm" : "text-xs")}>
                  {k.name}
                </span>
              ))}
            </div>
          )}
        </DashboardPanel>
        <DashboardPanel title="Languages" icon={<Languages size={16} />}>
          {taste.languages.length === 0 ? <p className="text-sm text-fg-muted">No language preference yet.</p> : (
            <DivergingBars ariaLabel="Language affinity" data={[...taste.languages].sort((a, b) => b.weight - a.weight).map((l) => ({ key: l.code, label: langName(l.code), weight: l.weight }))} />
          )}
        </DashboardPanel>
      </div>

      <DashboardPanel title="Your corrections" icon={<Sparkles size={16} />}
        actions={
          <div className="flex gap-2">
            <Button variant="ghost" size="sm" onClick={() => setOverrides(taste.overrides ?? [])} disabled={!dirty || saving}><Undo2 size={14} /> Discard</Button>
            <Button size="sm" onClick={() => void save()} disabled={!dirty || saving}><Save size={14} /> {saving ? "Saving…" : "Save corrections"}</Button>
          </div>
        }>
        <p className="mb-4 text-sm text-fg-muted">Corrections override what Ether learned, e.g. “not into horror”. −1 never recommends it; +1 always favours it.</p>
        {overrides.length === 0 ? (
          <EmptyState title="No corrections" description="Ether's learned profile is used as is. Add one below or click a genre above." className="mb-4 py-6" />
        ) : (
          <ul className="mb-4 space-y-2">
            {overrides.map((o) => (
              <li key={`${o.kind}:${o.value}`} className="flex flex-wrap items-center gap-3 rounded-lg border border-border-subtle bg-bg-subtle px-3 py-2">
                <Badge variant="secondary" className="capitalize">{o.kind}</Badge>
                <span className="min-w-28 text-sm font-medium text-fg">{o.kind === "person" ? personName(o.value) : o.value}</span>
                <Slider min={-1} max={1} step={0.05} value={o.weight} aria-label={`${o.value} weight`} className="min-w-32 flex-1"
                  onChange={(e) => setOverride(o.kind, o.value, e.currentTarget.valueAsNumber)} />
                <span className={cn("w-24 text-right text-xs tabular-nums", o.weight < 0 ? "text-danger" : "text-success")}>
                  {o.weight <= -0.95 ? "Never" : o.weight >= 0.95 ? "Always" : `${o.weight > 0 ? "+" : ""}${o.weight.toFixed(2)}`}
                </span>
                <Button variant="ghost" size="icon" className="size-7" onClick={() => setOverride(o.kind, o.value, null)} aria-label={`Remove correction for ${o.kind === "person" ? personName(o.value) : o.value}`}><X size={14} /></Button>
              </li>
            ))}
          </ul>
        )}
        <AddOverride onAdd={(kind, value, weight, name) => { if (name) setNames((n) => ({ ...n, [value]: name })); setOverride(kind, value, weight); }} />
      </DashboardPanel>
    </div>
  );
}

function AddOverride({ onAdd }: { onAdd: (kind: OverrideKind, value: string, weight: number, name?: string) => void }) {
  const [kind, setKind] = useState<OverrideKind>("genre");
  const [genre, setGenre] = useState("");
  const [weight, setWeight] = useState<"-1" | "1">("-1");
  return (
    <div className="rounded-lg border border-dashed border-border p-3">
      <p className="mb-2 text-sm font-medium text-fg">Add a correction</p>
      <div className="flex flex-wrap items-start gap-2">
        <Segmented ariaLabel="Correction type" size="sm" value={kind} onChange={setKind} options={[{ value: "genre", label: "Genre" }, { value: "person", label: "Person" }, { value: "keyword", label: "Theme" }]} />
        <Segmented ariaLabel="Correction direction" size="sm" value={weight} onChange={setWeight} options={[{ value: "-1", label: "Not into it" }, { value: "1", label: "Loves it" }]} />
      </div>
      <div className="mt-2">
        {kind === "genre" ? (
          <div className="flex gap-2">
            <div className="min-w-0 flex-1"><FacetSelect type="movie" facet="genre" value={genre} onChange={setGenre} ariaLabel="Genre to correct" /></div>
            <Button variant="outline" size="sm" className="h-10" disabled={!genre} onClick={() => { onAdd("genre", genre, Number(weight)); setGenre(""); }}><Plus size={14} /> Add</Button>
          </div>
        ) : (
          <LookupPicker kind={kind === "person" ? "person" : "keyword"} multiple={false} value={[]} ariaLabel={kind === "person" ? "Person to correct" : "Theme to correct"}
            onChange={(p) => { const pick = p[0]; if (pick) onAdd(kind, kind === "person" ? String(pick.id) : pick.name, Number(weight), pick.name); }} />
        )}
      </div>
    </div>
  );
}
