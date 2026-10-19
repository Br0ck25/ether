import { Button, PageContainer, PageHeader, SubtabNav, toast, Switch, Textarea } from "@braedonsaunders/appkit-ui";
import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api, apiFetch, errorMessage } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { EditPanel } from "../components/EditPanel";
import { ErrorPanel, LoadingRows } from "../components/feedback";
import { QualitySection } from "./settings/sections";
import type { EtherConfig } from "../lib/types";

type Policy = { family: string; dolbyVisionProfiles: number[] | null; subtitleCodecs: string[]; notes: string };
type ResolverPolicy = EtherConfig["resolver"] & { optimisticStart?: boolean; healthRace?: number; customFormats?: unknown[]; customFormatScores?: Record<string, number> };
const tabs = [{key:"profiles",label:"Quality profiles"},{key:"rules",label:"Client rules"},{key:"defaults",label:"Defaults"},{key:"startup",label:"Playback start"},{key:"scoring",label:"Advanced scoring"},{key:"compatibility",label:"Device capabilities"}];

export default function PlaybackPolicy() {
  const [search,setSearch]=useSearchParams();
  const tab=tabs.some(t=>t.key===search.get("tab"))?search.get("tab")!:"profiles";
  const cfg=useAsync(()=>api.getConfig(),[]);
  const matrix=useAsync(()=>apiFetch<{policies:Policy[]}>("/playback/policy"),[]);
  const [quality,setQuality]=useState<EtherConfig["quality"]|null>(null);
  const [resolver,setResolver]=useState<ResolverPolicy|null>(null);
  const [formats,setFormats]=useState("[]");const [scores,setScores]=useState("{}");
  const [busy,setBusy]=useState(false);const [error,setError]=useState<unknown>(null);
  useEffect(()=>{if(cfg.data){setQuality(cfg.data.quality);const r=cfg.data.resolver as ResolverPolicy;setResolver(r);setFormats(JSON.stringify(r.customFormats??[],null,2));setScores(JSON.stringify(r.customFormatScores??{},null,2));}},[cfg.data]);
  const restoreResolver = () => {
    if (!cfg.data) return;
    const saved = cfg.data.resolver as ResolverPolicy;
    setResolver(saved);
    setFormats(JSON.stringify(saved.customFormats ?? [], null, 2));
    setScores(JSON.stringify(saved.customFormatScores ?? {}, null, 2));
    setError(null);
  };
  async function saveQuality(next=quality){if(!next)return;setBusy(true);setError(null);try{await api.putSection("quality",next);await cfg.reload();toast.success("Quality settings saved");}catch(e){setError(e);toast.error(errorMessage(e));}finally{setBusy(false)}}
  async function saveResolver(){if(!resolver)return;setBusy(true);setError(null);try{const definitions:unknown=JSON.parse(formats);const overrides:unknown=JSON.parse(scores);if(!Array.isArray(definitions))throw Error("Definitions must be a JSON array.");if(!overrides||Array.isArray(overrides)||typeof overrides!=="object"||Object.values(overrides).some(v=>typeof v!=="number"||!Number.isInteger(v)))throw Error("Scores must map format IDs to whole numbers.");await api.putSection("resolver",{...resolver,customFormats:definitions,customFormatScores:overrides});await cfg.reload();toast.success("Playback settings saved");}catch(e){setError(e);toast.error(errorMessage(e));}finally{setBusy(false)}}
  const footer=<><ErrorPanelIf error={error}/><Button disabled={busy} onClick={()=>void saveQuality()}>{busy?"Saving…":"Save quality settings"}</Button></>;
  return <PageContainer className="space-y-6">
    <PageHeader title="Playback" description="Choose the quality you want. Ether adapts each release to the device playing it."/>
    <div className="border-b border-border"><SubtabNav ariaLabel="Playback tools" active={tab} onSelect={key=>setSearch({tab:key})} tabs={tabs}/></div>
    {cfg.error?<ErrorPanel error={cfg.error}/>:!quality||!resolver?<LoadingRows/>:<>
      {(tab==="profiles"||tab==="rules"||tab==="defaults")&&<QualitySection view={tab} value={quality} onChange={setQuality} errors={{}} footer={footer} onCommit={saveQuality} onDiscard={()=>setQuality(cfg.data?.quality??quality)}/>}
      {tab==="rules"&&<p className="text-sm text-fg-muted">Rules choose among your quality profiles. Device codec support is still checked before playback. <Link className="text-primary underline" to="?tab=compatibility">View device capabilities</Link></p>}
      {tab==="startup"&&<EditPanel onDiscard={restoreResolver} title="Playback start" description="Ranked selection is the default. Advanced startup modes trade extra indexer requests for less waiting." action="Edit startup behavior" summary={resolver.optimisticStart?"Optimistic start enabled":resolver.healthRace?`First healthy of ${resolver.healthRace}`:"Ranked selection"}>
        <label className="flex items-center gap-3 text-sm"><Switch type="checkbox" checked={resolver.optimisticStart??false} onChange={e=>setResolver({...resolver,optimisticStart:e.target.checked})}/>Start the top release before its health check completes</label>
        <p className="text-sm text-fg-muted">Missing articles still trigger failover. Background failures mark the release bad for later requests.</p>
        <label className="block space-y-2 text-sm">Concurrent checks<select className="w-full rounded-md border border-border bg-surface p-2" value={resolver.healthRace??0} onChange={e=>setResolver({...resolver,healthRace:Number(e.target.value)})}><option value={0}>Ranked selection</option>{[2,3,4].map(n=><option key={n} value={n}>First healthy of {n}</option>)}</select></label>
        <p className="text-xs text-fg-muted">Concurrent checks can use additional NZB grabs. Detail-page warming has a separate small budget.</p><ErrorPanelIf error={error}/><Button disabled={busy} onClick={()=>void saveResolver()}>Save startup settings</Button>
      </EditPanel>}
      {tab==="scoring"&&<><EditPanel onDiscard={restoreResolver} title="Custom release scoring" description="Import native TRaSH custom formats and assign scores. Existing quality profiles remain the starting point." action="Edit custom formats" summary={`${resolver.customFormats?.length??0} custom formats`}>
        <label className="block space-y-2 text-sm">Custom format definitions<Textarea className="min-h-64 w-full rounded-md border border-border bg-surface p-3 font-mono text-xs" value={formats} onChange={e=>setFormats(e.target.value)}/></label>
        <label className="block space-y-2 text-sm">Custom format score overrides<Textarea className="min-h-28 w-full rounded-md border border-border bg-surface p-3 font-mono text-xs" value={scores} onChange={e=>setScores(e.target.value)}/></label><ErrorPanelIf error={error}/><Button disabled={busy} onClick={()=>void saveResolver()}>Save release scoring</Button>
      </EditPanel><Link to="/traces" className="inline-block text-sm text-primary underline">Inspect release scores and rejection reasons</Link><Link to="/settings/resolver" className="ml-4 inline-block text-sm text-primary underline">More resolver settings</Link></>}
      {tab==="compatibility"&&<><p className="max-w-3xl text-sm text-fg-muted">Built-in compatibility guidance is a reference. Each Plex app reports its actual codecs, display and receiver support. To choose quality for a device, use <Link to="?tab=rules" className="text-primary underline">Client rules</Link>.</p>{matrix.error?<ErrorPanel error={matrix.error}/>:!matrix.data?<LoadingRows/>:<div className="overflow-x-auto rounded-xl border border-border"><table className="w-full text-left text-sm"><thead><tr><th className="p-3">Client</th><th className="p-3">Dolby Vision</th><th className="p-3">Subtitles</th><th className="p-3">Behavior</th></tr></thead><tbody>{matrix.data.policies.map(p=><tr key={p.family} className="border-t border-border"><td className="p-3 font-medium">{p.family}</td><td className="p-3">{p.dolbyVisionProfiles?.join(", ")||"Reported by client"}</td><td className="p-3">{p.subtitleCodecs.join(", ")}</td><td className="p-3">{p.notes}</td></tr>)}</tbody></table></div>}</>}
    </>}
  </PageContainer>;
}
function ErrorPanelIf({error}:{error:unknown}){return error?<ErrorPanel error={error}/>:null}
