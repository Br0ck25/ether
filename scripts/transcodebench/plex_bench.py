#!/usr/bin/env python3
"""Authentic Plex CPU transcode baseline driver (stdlib only).

Launches the isolated lab PMS as an owned child, requests a genuine
/video/:/transcode/universal decision + HLS start, downloads segments,
samples the owned process tree from /proc, captures the authentic
Plex Transcoder cmdline from owned descendants, and writes sanitized
JSON results. Phases are separated: setup / encode / verify.
No Ether spike mode is used. No claimed account is required for CPU.
"""
from __future__ import annotations
import argparse, hashlib, json, os, re, signal, subprocess, sys, time, uuid
import concurrent.futures, threading, urllib.error, urllib.parse, urllib.request
import xml.etree.ElementTree as ET
from dataclasses import dataclass, field, asdict, replace
from pathlib import Path

SENSITIVE = re.compile(r'(?i)(x-plex-token|plexonline token|authorization|token|password|secret)(["\'=:\s]+)([^\s"\'&,;]+)')
def sanitize(text: str) -> str:
    if not text: return text
    text = re.sub(r'(?i)(Bearer\s+)[^\s"\']+', r'\1<redacted>', text)
    text = SENSITIVE.sub(lambda m: f"{m.group(1)}{m.group(2)}<redacted>", text)
    text = re.sub(r'(?i)(X-Plex-Token=)[^&\s"\']+', r'\1<redacted>', text)
    return text

class BenchError(RuntimeError): pass

@dataclass
class Profile:
    video_codec: str = "h264"
    video_resolution: str = "640x360"
    max_video_bitrate_kbps: int = 2000
    video_quality: int = 100
    audio_codec: str = "aac"
    protocol: str = "hls"
    # Generic.xml ships NO TranscodeTargets -> PMS 4005 "No conversion profile
    # found for protocol hls". Android.xml ships hls/mpegts/h264/aac.
    platform: str = "Android"
    client_identifier: str = "ether-lab-bench"
    def client_profile_extra(self) -> str:
        w, h = self.video_resolution.lower().split("x", 1)
        return (
            "add-transcode-target(type=videoProfile&context=streaming"
            f"&protocol={self.protocol}&container=mpegts&videoCodec={self.video_codec}"
            f"&audioCodec={self.audio_codec}&replace=true)"
            f"+add-limitation(scope=videoCodec&scopeName={self.video_codec}"
            f"&type=upperBound&name=video.width&value={w})"
            f"+add-limitation(scope=videoCodec&scopeName={self.video_codec}"
            f"&type=upperBound&name=video.height&value={h})"
        )
    def headers(self) -> dict:
        return {"X-Plex-Platform": self.platform, "X-Plex-Client-Identifier": self.client_identifier,
                "X-Plex-Client-Profile-Extra": self.client_profile_extra(),
                "X-Plex-Product": "EtherLabBench", "X-Plex-Version": "1.0",
                "X-Plex-Device": "Linux", "X-Plex-Provides": "player"}
    def decision_params(self, rating_key: str, session: str) -> dict:
        return {
            "path": f"/library/metadata/{rating_key}", "mediaIndex": "0", "partIndex": "0",
            "protocol": self.protocol, "fastSeek": "1", "directPlay": "0", "directStream": "0",
            "videoQuality": str(self.video_quality), "maxVideoBitrate": str(self.max_video_bitrate_kbps),
            "videoResolution": self.video_resolution, "videoCodec": self.video_codec,
            "audioCodec": self.audio_codec, "session": session,
            "X-Plex-Platform": self.platform, "X-Plex-Client-Identifier": self.client_identifier,
            "X-Plex-Client-Profile-Extra": self.client_profile_extra(),
            "X-Plex-Product": "EtherLabBench", "X-Plex-Version": "1.0",
            "X-Plex-Device": "Linux", "X-Plex-Provides": "player",
        }

DECISION_OK = {"1000", "1001"}  # 1000=playable, 1001=direct play N/A but conversion OK
def validate_decision(root) -> None:
    """Reject transcode failures before start.m3u8 (2000/3000/4005 = failure)."""
    general = root.get("generalDecisionCode")
    if general is not None and general not in DECISION_OK:
        raise BenchError(f"decision rejected: generalDecisionCode={general} "
                         f"text={root.get('generalDecisionText')} "
                         f"transcodeDecisionCode={root.get('transcodeDecisionCode')} "
                         f"text={root.get('transcodeDecisionText')}")
    transcode = root.get("transcodeDecisionCode")
    if transcode is not None and transcode not in DECISION_OK:
        raise BenchError(f"decision transcode failed: transcodeDecisionCode={transcode} "
                         f"text={root.get('transcodeDecisionText')}")
    for el in root.iter():
        for k, v in el.attrib.items():
            if k == "decision" and v == "none" and el.tag in ("Part", "Media"):
                raise BenchError(f"decision rejected: <{el.tag} decision='none'>")

@dataclass
class Config:
    lab_root: str = "/data/bb/thread-storage/thr_vhnx7wc862/lab"
    base_url: str = "http://127.0.0.1:32400"
    rating_key: str = "1"
    duration_s: float = 10.0
    mode: str = "fast"  # fast | paced
    concurrency: int = 1
    repeats: int = 1
    timeout_s: float = 120.0
    poll_interval_s: float = 0.25
    launch: bool = True
    startup_timeout_s: float = 90.0
    preserve_media: bool = False
    out_dir: str = ""
    ffprobe: str = ""
    profile: Profile = field(default_factory=Profile)

# ---------- /proc helpers ----------
def _read(path):
    try: return Path(path).read_text(errors="replace")
    except OSError: return ""

def proc_tree(root_pid: int) -> list[int]:
    ppid = {}
    for e in Path("/proc").glob("[0-9]*"):
        try:
            parts = _read(e/"stat").rsplit(") ",1)
            if len(parts)<2: continue
            ppid[int(e.name)] = int(parts[1].split()[1])
        except Exception: continue
    out, stack = [], [root_pid]
    children = {}
    for pid, pp in ppid.items(): children.setdefault(pp, []).append(pid)
    seen=set()
    while stack:
        p=stack.pop()
        if p in seen: continue
        seen.add(p); out.append(p); stack.extend(children.get(p,[]))
    return out

def sample_tree(root_pid: int) -> dict:
    total_cpu=0; total_rss=0; total_read=0; total_write=0; procs=[]
    clk=os.sysconf("SC_CLK_TCK")
    for pid in proc_tree(root_pid):
        stat=_read(f"/proc/{pid}/stat"); 
        if not stat: continue
        try:
            r=stat.rsplit(") ",1)[1].split(); utime=int(r[11]); stime=int(r[12])
            comm=stat[stat.find("(")+1:stat.rfind(")")]
        except Exception: continue
        rss=0
        for line in _read(f"/proc/{pid}/status").splitlines():
            if line.startswith("VmRSS:"): rss=int(line.split()[1])*1024
        rd=wr=0
        for line in _read(f"/proc/{pid}/io").splitlines():
            if line.startswith("read_bytes:"): rd=int(line.split()[1])
            if line.startswith("write_bytes:"): wr=int(line.split()[1])
        total_cpu+=utime+stime; total_rss+=rss; total_read+=rd; total_write+=wr
        procs.append({"pid":pid,"comm":comm,"cpu_ticks":utime+stime,"rss_bytes":rss,"read_bytes":rd,"write_bytes":wr})
    return {"cpu_seconds": total_cpu/clk, "rss_bytes": total_rss, "read_bytes": total_read,
            "write_bytes": total_write, "processes": procs}

def capture_transcoder_cmdlines(root_pid: int) -> list[dict]:
    out=[]
    for pid in proc_tree(root_pid):
        raw=_read(f"/proc/{pid}/cmdline")
        if not raw: continue
        argv=[a for a in raw.split("\x00") if a]
        if argv and "Transcoder" in " ".join(argv[:2]):
            out.append({"pid":pid,"argv":[sanitize(a) for a in argv],"cmdline":sanitize(" ".join(argv))})
    return out

# ---------- playlist ----------
def parse_playlist(text: str, base_url: str):
    """Return (is_master, media_playlist_url_or_None, segments, extinf_total, has_endlist).
    segments: list of (uri_resolved, extinf_seconds). Raises BenchError on unknown duration."""
    lines=[l.strip() for l in text.splitlines() if l.strip()]
    if not any(l.startswith("#EXTM3U") for l in lines): raise BenchError("playlist missing #EXTM3U")
    if any(l.startswith("#EXT-X-STREAM-INF") for l in lines):
        for l in lines:
            if not l.startswith("#"): return True, urllib.parse.urljoin(base_url, l), [], 0.0, False
        raise BenchError("master playlist has no variant URI")
    segs=[]; total=0.0; pending=None; end=False
    for l in lines:
        if l.startswith("#EXT-X-ENDLIST"): end=True
        elif l.startswith("#EXTINF:"):
            try: pending=float(l.split(":",1)[1].split(",")[0])
            except ValueError: raise BenchError(f"unparseable EXTINF: {l}")
        elif not l.startswith("#"):
            if pending is None: raise BenchError("segment URI without preceding EXTINF (unknown duration)")
            segs.append((urllib.parse.urljoin(base_url,l), pending)); total+=pending; pending=None
    return False, None, segs, total, end

# ---------- HTTP ----------
def http_get(url: str, timeout: float, headers=None):
    req=urllib.request.Request(url, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, dict(r.headers), r.read()
    except urllib.error.HTTPError as e:
        try:
            detail=sanitize(e.read().decode(errors='replace')[:500])
        finally:
            e.close()
        raise BenchError(f"HTTP {e.code} for {sanitize(url)}: {detail}")
    except Exception as e:
        raise BenchError(f"HTTP error for {sanitize(url)}: {e}")

def sha256_file(p: Path) -> str:
    h=hashlib.sha256()
    with open(p,"rb") as f:
        for chunk in iter(lambda: f.read(1<<20), b""): h.update(chunk)
    return h.hexdigest()

# ---------- PMS lifecycle ----------
class PmsProcess:
    def __init__(self, cfg: Config):
        self.cfg=cfg; self.proc=None; self.pid=None; self.pgid=None
    def start(self):
        lab=Path(self.cfg.lab_root); pms=lab/"plex/root/usr/lib/plexmediaserver"
        if not (pms/"Plex Media Server").exists(): raise BenchError(f"PMS binary missing under {pms}")
        env=dict(os.environ); env.pop("LD_LIBRARY_PATH",None)
        env.update({"PLEX_MEDIA_SERVER_APPLICATION_SUPPORT_DIR":str(lab/"plex/config"),
                    "PLEX_MEDIA_SERVER_HOME":str(pms),
                    "PLEX_MEDIA_SERVER_TMPDIR":str(lab/"plex/transcode"),
                    "LD_LIBRARY_PATH":str(pms/"lib")})
        self.proc=subprocess.Popen(["./Plex Media Server"], cwd=str(pms), env=env,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
        self.pid=self.proc.pid; self.pgid=os.getpgid(self.pid)
        return self.pid
    def wait_identity(self):
        deadline=time.monotonic()+self.cfg.startup_timeout_s; last=""
        while time.monotonic()<deadline:
            if self.proc and self.proc.poll() is not None: raise BenchError(f"PMS exited early rc={self.proc.returncode}")
            try:
                s,_,b=http_get(self.cfg.base_url+"/identity", timeout=5)
                if s==200:
                    root=ET.fromstring(b); return {"claimed":root.get("claimed"),"version":root.get("version"),
                                                   "machineIdentifier_present": bool(root.get("machineIdentifier"))}
            except BenchError as e: last=str(e)
            except Exception as e: last=str(e)
            time.sleep(0.5)
        raise BenchError(f"PMS identity not ready in {self.cfg.startup_timeout_s}s: {last}")
    def stop(self):
        if not self.proc: return
        try:
            if self.proc.poll() is None:
                os.killpg(self.pgid, signal.SIGTERM)
                try: self.proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    os.killpg(self.pgid, signal.SIGKILL); self.proc.wait(timeout=10)
        except (ProcessLookupError, PermissionError): pass

# ---------- one transcode session ----------
def run_session(cfg: Config, root_pid: int|None, out_dir: Path, idx: int) -> dict:
    session=uuid.uuid4().hex
    # PMS replaces the previous transcode for the same client identifier,
    # even when the URL's session GUID differs. Model independent viewers.
    prof=replace(cfg.profile, client_identifier=f"{cfg.profile.client_identifier}-{session}")
    params=prof.decision_params(cfg.rating_key, session)
    qs=urllib.parse.urlencode(params)
    result={"session_index":idx,"session_id":session,"profile":asdict(prof),
            "requested_duration_s":cfg.duration_s,"mode":cfg.mode}
    stop_url=f"{cfg.base_url}/video/:/transcode/universal/stop?{urllib.parse.urlencode({'session':session})}"
    t_setup=time.monotonic()
    argv_stop=None; argv_thread=None
    try:
        # decision (setup phase, not encode)
        s,_,body=http_get(f"{cfg.base_url}/video/:/transcode/universal/decision?{qs}", timeout=min(30,cfg.timeout_s), headers=prof.headers())
        if s!=200: raise BenchError(f"decision HTTP {s}")
        result["decision_xml_sanitized"]=sanitize(body.decode(errors="replace"))[:8000]
        result["profile_extra"]=prof.client_profile_extra()
        try:
            root=ET.fromstring(body)
            validate_decision(root)  # reject 2000/4005 etc before start
            result["decision"]={k:sanitize(v) for k,v in root.attrib.items()}
            medias=[]
            for m in root.iter("Media"):
                medias.append({k:sanitize(v) for k,v in m.attrib.items()})
            result["decision_media"]=medias
            dec=root.get("generalDecisionCode") or root.get("transcodeDecisionCode") or ""
            result["decision_code"]=dec
        except ET.ParseError as e: raise BenchError(f"decision XML parse failed: {e}")
        result["setup_s"]=time.monotonic()-t_setup

        # encode phase; capture authentic argv early/often (encoder can be short-lived)
        cpu_start=sample_tree(root_pid) if root_pid else None
        t_enc=time.monotonic(); first_latency=None; last_delivery=None
        argv_by_key={}; argv_stop=threading.Event()
        def argv_poller():
            while not argv_stop.is_set():
                if root_pid:
                    for c in capture_transcoder_cmdlines(root_pid):
                        argv_by_key[(c["pid"], c["cmdline"])] = c
                argv_stop.wait(0.05)
        argv_thread=threading.Thread(target=argv_poller, daemon=True); argv_thread.start()
        start_url=f"{cfg.base_url}/video/:/transcode/universal/start.m3u8?{qs}"
        s,_,body=http_get(start_url, timeout=min(30,cfg.timeout_s), headers=prof.headers())
        if s!=200: raise BenchError(f"start.m3u8 HTTP {s}")
        is_master, media_url, segs, total, end = parse_playlist(body.decode(errors="replace"), start_url)
        if is_master:
            s,_,body=http_get(media_url, timeout=min(30,cfg.timeout_s))
            if s!=200: raise BenchError(f"media playlist HTTP {s}")
            _,_,segs,total,end=parse_playlist(body.decode(errors="replace"), media_url)
            playlist_url=media_url
        else: playlist_url=start_url

        downloaded={}  # uri -> (bytes, extinf)
        seen=set(); order={}; peak_rss=None
        samples=[]; deadline=t_enc+cfg.timeout_s
        media_dir=out_dir/f"session-{idx}"
        if cfg.preserve_media: media_dir.mkdir(parents=True, exist_ok=True)
        lock=threading.Lock()
        def fetch(item):
            nonlocal first_latency, last_delivery
            uri, extinf = item; ordinal=order[uri]
            # PMS lists segments before they are fully written; retry 404s briefly.
            deadline_seg=time.monotonic()+15.0; data=None
            while True:
                try:
                    st,_,data=http_get(uri, timeout=min(30,cfg.timeout_s)); break
                except BenchError as e:
                    if "HTTP 404" in str(e) and time.monotonic()<deadline_seg:
                        time.sleep(0.5); continue
                    raise
            if st!=200: raise BenchError(f"segment HTTP {st}")
            if not data: raise BenchError(f"empty segment {sanitize(uri)}")
            with lock:
                downloaded[uri]=(len(data),extinf)
                last_delivery=time.monotonic()
                if ordinal==0 and first_latency is None: first_latency=last_delivery-t_enc  # first segment body complete
            if cfg.preserve_media:  # stable playlist-order name, not completion order
                (media_dir/f"seg-{ordinal:05d}.ts").write_bytes(data)
            return len(data)

        try:
            while time.monotonic()<deadline:
                try:
                    s,_,body=http_get(playlist_url, timeout=min(15,cfg.timeout_s))
                    if s!=200: raise BenchError(f"playlist HTTP {s}")
                    _,_,segs,total,end=parse_playlist(body.decode(errors="replace"), playlist_url)
                except BenchError:
                    if downloaded: raise
                    time.sleep(cfg.poll_interval_s); continue
                # assign stable ordinals in playlist order; cap queued media at requested duration
                queued_media=sum(order_ext for order_ext in [downloaded[u][1] for u in downloaded]) if downloaded else 0.0
                new=[]
                for uri, extinf in segs:
                    if uri not in order: order[uri]=len(order)
                    if uri in seen: continue
                    if queued_media>=cfg.duration_s: break
                    seen.add(uri); new.append((uri,extinf)); queued_media+=extinf
                if new:
                    if cfg.mode=="paced":
                        for item in new:
                            # Request the next segment at its playback boundary;
                            # the first segment is requested immediately.
                            elapsed_media=sum(v[1] for v in downloaded.values())
                            delay=t_enc+elapsed_media-time.monotonic()
                            if delay>0: time.sleep(min(delay,max(0,deadline-time.monotonic())))
                            fetch(item)
                    else:
                        # Sequential segment reads match normal playback and
                        # avoid treating speculative future requests as seeks.
                        for item in new: fetch(item)
                if root_pid:
                    smp=sample_tree(root_pid); samples.append(smp)
                    peak_rss=smp["rss_bytes"] if peak_rss is None else max(peak_rss,smp["rss_bytes"])
                actual_media=sum(v[1] for v in downloaded.values())
                if actual_media>=cfg.duration_s: break
                if end and not new: break
                time.sleep(cfg.poll_interval_s)
            else:
                raise BenchError(f"session timeout after {cfg.timeout_s}s")
            cpu_end=sample_tree(root_pid) if root_pid else None
        finally:
            argv_stop.set(); argv_thread.join(timeout=2)
        cmdlines=list(argv_by_key.values())
        if root_pid and not cmdlines:
            raise BenchError("authentic encode claimed but no Plex Transcoder argv captured from owned descendants")

        actual_media=sum(v[1] for v in downloaded.values())
        if not downloaded: raise BenchError("no segments downloaded")
        if actual_media<=0: raise BenchError("unknown media duration: EXTINF total is zero")
        result.update({"encode_s":last_delivery-t_enc,"first_segment_latency_s":first_latency,
            "measurement_finished_monotonic_s":last_delivery,
            "first_segment_latency_definition":"seconds from encode start until segment 0 body fully downloaded",
            "actual_media_s":round(actual_media,3),"segment_count":len(downloaded),
            "output_bytes":sum(v[0] for v in downloaded.values()),
            "peak_rss_bytes":peak_rss,"samples":samples[-20:],
            "cpu_sampling":"available" if samples else "unavailable",
            "transcoder_cmdlines":cmdlines,"playlist_endlist":end})
        if cpu_start and root_pid and samples:
            delta, io_delta, valid = compute_tree_delta(cpu_start, cpu_end)
            if valid:
                result["tree_cpu_delta_s"]=delta; result["tree_io_delta"]=io_delta
            else:  # children exited -> cumulative /proc counters drop; never invent savings
                result["tree_cpu_delta_s"]=None; result["tree_io_delta"]=None
                result["cpu_sampling"]="unavailable"
                result["cpu_sampling_reason"]="invalid/incomplete tree delta (negative; children likely exited)"
        else:
            result["tree_cpu_delta_s"]=None; result["tree_io_delta"]=None
            result["cpu_sampling"]="unavailable"
        # actual vs requested
        actual_codec=None
        for m in result.get("decision_media",[]):
            actual_codec=m.get("videoCodec") or actual_codec
        result["requested_vs_actual"]={"requested_video_codec":prof.video_codec,"actual_video_codec":actual_codec,
            "requested_resolution":prof.video_resolution,
            "actual_resolution": (f"{result['decision_media'][0].get('width')}x{result['decision_media'][0].get('height')}" if result.get("decision_media") and result["decision_media"][0].get("width") and result["decision_media"][0].get("height") else (result["decision_media"][0].get("videoResolution") if result.get("decision_media") else None)),
            "hw_note":"CPU-only host (no HW encode entrypoint); inspect transcoder cmdline for vaapi/nvenc tokens",
            "transcoder_used_hw": any(("vaapi" in c["cmdline"] or "nvenc" in c["cmdline"]) for c in cmdlines)}
        return result
    finally:
        if argv_stop is not None: argv_stop.set()
        if argv_thread is not None: argv_thread.join(timeout=2)
        # stop request each session in finally; reflect actual success
        try:
            stop_status,_,_ = http_get(stop_url, timeout=5)
            result["stopped_session"] = stop_status in (200, 204)
            result["stop_status"] = stop_status
        except Exception:
            result["stopped_session"] = False
            result["stop_status"] = None

def compute_tree_delta(start: dict, end: dict):
    """(cpu_delta_s, io_delta, valid). Negative/incomplete -> invalid, caller labels unavailable."""
    try:
        cpu = round(end["cpu_seconds"]-start["cpu_seconds"], 3)
        io = {"read_bytes": end["read_bytes"]-start["read_bytes"],
              "write_bytes": end["write_bytes"]-start["write_bytes"]}
    except Exception:
        return None, None, False
    if cpu < 0 or io["read_bytes"] < 0 or io["write_bytes"] < 0: return None, None, False
    if not end.get("processes"): return None, None, False
    return cpu, io, True

def verify_output(cfg: Config, out_dir: Path, sessions: list) -> dict:
    """Separate quality phase (not counted in encode). Uses ffprobe if available."""
    v={"checked":False}
    if not cfg.preserve_media: v["reason"]="preserve_media off"; return v
    ffprobe=cfg.ffprobe or str(Path(cfg.lab_root)/"ether/ffprobe")
    if not Path(ffprobe).exists(): v["reason"]=f"ffprobe not found: {ffprobe}"; return v
    per_session=[]
    for idx, sess in enumerate(sessions):
        sidx = sess.get("session_index", idx) if isinstance(sess, dict) else idx
        d=out_dir/f"session-{sidx}"; segs=sorted(d.glob("seg-*.ts"))
        if not segs:
            per_session.append({"session_index":sidx,"checked":False,"matches_codec":False,
                                "matches_resolution":False,"reason":"no preserved segments"}); continue
        concat=b"".join(p.read_bytes() for p in segs)
        tmp=d/"concat.ts"; tmp.write_bytes(concat)
        try:
            r=subprocess.run([ffprobe,"-v","error","-select_streams","v:0","-show_entries",
                "stream=codec_name,width,height","-of","json",str(tmp)],capture_output=True,text=True,timeout=30)
            if r.returncode!=0: raise BenchError(f"ffprobe decode check failed: {sanitize(r.stderr[:500])}")
            info=json.loads(r.stdout); st=(info.get("streams") or [{}])[0]
            per_session.append({"session_index":sidx,"checked":True,"codec":st.get("codec_name"),
                "width":st.get("width"),"height":st.get("height"),"expected_codec":cfg.profile.video_codec,
                "matches_codec": st.get("codec_name")==cfg.profile.video_codec,
                "matches_resolution": f"{st.get('width')}x{st.get('height')}"==cfg.profile.video_resolution})
        except BenchError: raise
        except Exception as e: raise BenchError(f"ffprobe failed: {e}")
    if not per_session: v["reason"]="no sessions"; return v
    first=per_session[0]
    return {"checked": all(x["checked"] for x in per_session),
            "codec": first.get("codec"), "width": first.get("width"), "height": first.get("height"),
            "expected_codec": cfg.profile.video_codec,
            "matches_codec": all(x.get("matches_codec") for x in per_session),
            "matches_resolution": all(x.get("matches_resolution") for x in per_session),
            "sessions": per_session}

def main(argv=None):
    ap=argparse.ArgumentParser(description="Authentic Plex CPU transcode baseline")
    ap.add_argument("--lab-root",default=Config.lab_root); ap.add_argument("--base-url",default=Config.base_url)
    ap.add_argument("--rating-key",default="1"); ap.add_argument("--duration",type=float,default=10.0)
    ap.add_argument("--mode",choices=["fast","paced"],default="fast")
    ap.add_argument("--concurrency",type=int,default=1); ap.add_argument("--repeats",type=int,default=1)
    ap.add_argument("--timeout",type=float,default=120.0); ap.add_argument("--no-launch",action="store_true")
    ap.add_argument("--preserve-media",action="store_true"); ap.add_argument("--out",default="")
    ap.add_argument("--ffprobe",default=""); ap.add_argument("--video-codec",default="h264")
    ap.add_argument("--video-resolution",default="640x360"); ap.add_argument("--max-video-bitrate",type=int,default=2000)
    ap.add_argument("--audio-codec",default="aac")
    a=ap.parse_args(argv)
    if a.duration<=0 or a.concurrency<1 or a.repeats<1: ap.error("duration/concurrency/repeats must be positive")
    out_dir=Path(a.out) if a.out else Path(f"/data/bb/thread-storage/transcodebench-{int(time.time())}")
    out_dir.mkdir(parents=True,exist_ok=True)
    cfg=Config(lab_root=a.lab_root,base_url=a.base_url,rating_key=a.rating_key,duration_s=a.duration,
        mode=a.mode,concurrency=a.concurrency,repeats=a.repeats,timeout_s=a.timeout,launch=not a.no_launch,
        preserve_media=a.preserve_media,out_dir=str(out_dir),ffprobe=a.ffprobe,
        profile=Profile(video_codec=a.video_codec,video_resolution=a.video_resolution,
                        max_video_bitrate_kbps=a.max_video_bitrate,audio_codec=a.audio_codec))
    fixture=Path(cfg.lab_root)/"media/source.mkv"
    meta={"tool":"plex_bench/1","started_at":time.strftime("%Y-%m-%dT%H:%M:%SZ",time.gmtime()),
          "config":{**asdict(cfg),"profile":asdict(cfg.profile)},
          "source":{"fixture":str(fixture),"sha256":sha256_file(fixture) if fixture.exists() else None,
                    "bytes":fixture.stat().st_size if fixture.exists() else None},
          "host_label":"CPU-only, AMD Ryzen 7 2700X, Radeon HD 7450 (decode-only VA), software encode"}
    pms=None; root_pid=None; sessions=[]; waves=[]
    try:
        if cfg.launch:
            pms=PmsProcess(cfg); meta["pms_pid"]=pms.start(); root_pid=pms.pid
            meta["identity"]=pms.wait_identity()
        else:
            s,_,b=http_get(cfg.base_url+"/identity",timeout=10)
            root=ET.fromstring(b); meta["identity"]={"claimed":root.get("claimed"),"version":root.get("version")}
        meta["pms_version"]=meta.get("identity",{}).get("version")
        # concurrency = simultaneous sessions per wave; repeats = waves.
        # Wave wall is setup-inclusive and is the ONLY denominator for aggregate speed.
        for wave in range(cfg.repeats):
            t_wave=time.monotonic(); wave_sessions=[]
            with concurrent.futures.ThreadPoolExecutor(max_workers=cfg.concurrency) as ex:
                futs=[ex.submit(run_session,cfg,root_pid,out_dir,wave*cfg.concurrency+i) for i in range(cfg.concurrency)]
                for f in futs: wave_sessions.append(f.result())
            wall=time.monotonic()-t_wave
            completed=[s.get("measurement_finished_monotonic_s") for s in wave_sessions]
            delivery_wall=max(completed)-t_wave if completed and all(v is not None for v in completed) else None
            media_total=round(sum(s.get("actual_media_s") or 0 for s in wave_sessions),3)
            wall_r=round(wall,3)
            waves.append({"wave":wave,"wall_s":wall_r,"concurrency":cfg.concurrency,
                          "delivery_wall_s":round(delivery_wall,3) if delivery_wall is not None else None,
                          "aggregate_media_per_delivery_s":round(media_total/delivery_wall,3) if delivery_wall and delivery_wall>0 else None,
                          "sessions":len(wave_sessions),"actual_media_total_s":media_total,
                          "aggregate_media_per_wall_s": round(media_total/wall_r,3) if wall_r>0 else None})
            sessions.extend(wave_sessions)
        meta["sessions"]=sessions; meta["waves"]=waves
        meta["verify"]=verify_output(cfg,out_dir,sessions)  # separate phase
        v=meta["verify"]
        if v.get("checked") and not (v.get("matches_codec") and v.get("matches_resolution")):
            meta["ok"]=False; meta["error"]="verify mismatch: codec/resolution does not match profile in at least one session"
        elif cfg.preserve_media and sessions and not v.get("checked"):
            meta["ok"]=False; meta["error"]=f"verify incomplete: {v.get('reason','not all sessions checked')}"
        else:
            meta["ok"]=True
    except Exception as e:
        meta["ok"]=False; meta["error"]=sanitize(str(e)); meta["sessions"]=sessions; meta["waves"]=waves
    finally:
        if pms: pms.stop(); meta["pms_stopped"]=True
    (out_dir/"result.json").write_text(json.dumps(meta,indent=2))
    print(json.dumps({"ok":meta["ok"],"out":str(out_dir),"sessions":len(sessions),
                      **({"error":meta["error"]} if not meta["ok"] else {}),
                      "actual_media_s":[s.get("actual_media_s") for s in sessions],
                      "waves":waves}))
    return 0 if meta["ok"] else 1

if __name__=="__main__": sys.exit(main())
