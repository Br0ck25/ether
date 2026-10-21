#!/usr/bin/env python3
"""Native Windows Plex NVENC baseline driver (stdlib only).

Reuses the authentic PMS HTTP flow from plex_bench.py (decision + HLS
start, segment download, EXTINF accounting, sanitized JSON) and replaces
only the Linux-specific pieces: /proc tree sampling, PMS process
lifecycle, and filesystem/registry isolation.

Isolation model (Windows PMS has no env-var data-dir override):
  HKCU\\Software\\Plex, Inc.\\Plex Media Server  value LocalAppDataPath
  (REG_SZ) names the parent under which PMS creates its
  "Plex Media Server" data directory. The setup script seeds it to
  D:\\EtherTranscodeLab\\Plex\\data BEFORE first launch; this driver
  refuses to launch if the live value does not match the plan, so a
  mis-seeded registry can never silently write to %LOCALAPPDATA% on C:.
No service or startup entry is used: PMS runs as an owned child process.
"""
from __future__ import annotations
import argparse, ctypes, dataclasses, json, ntpath, os, subprocess, sys, threading, time, uuid
import concurrent.futures, urllib.parse, xml.etree.ElementTree as ET
from dataclasses import asdict
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import plex_bench as pb

REG_KEY = r"Software\Plex, Inc.\Plex Media Server"
REG_VALUE = "LocalAppDataPath"
DEFAULT_LAB = r"D:\EtherTranscodeLab\Plex"


def isolation_plan(lab_root: str) -> dict:
    """Pure plan (ntpath so it is identical/testable on any host OS)."""
    lab = lab_root.rstrip("\\/")
    data_parent = ntpath.join(lab, "data")
    return {
        "lab_root": lab,
        "app_dir": ntpath.join(lab, "app"),
        "pms_exe": ntpath.join(lab, "app", "Plex Media Server.exe"),
        "transcoder_exe": ntpath.join(lab, "app", "Plex Transcoder.exe"),
        "registry_key": "HKCU\\" + REG_KEY,
        "registry_value": REG_VALUE,
        "local_app_data_path": data_parent,
        "expected_data_dir": ntpath.join(data_parent, "Plex Media Server"),
        "transcode_dir": ntpath.join(lab, "transcode"),
        "media_dir": ntpath.join(lab, "media"),
        "fixture": ntpath.join(lab, "media", "source.mkv"),
        "results_dir": ntpath.join(lab, "results"),
    }


def profile_extra(video_codec: str, audio_codec: str, video_resolution: str,
                  protocol: str = "hls", container: str = "mpegts") -> str:
    """Exact X-Plex-Client-Profile-Extra value (verbatim shape from the
    corrected Linux driver, cd499e44+5a277350). Generic has no
    TranscodeTargets; this explicit target is what makes PMS transcode."""
    width, height = video_resolution.lower().split("x", 1)
    return (
        f"add-transcode-target(type=videoProfile&context=streaming&protocol={protocol}"
        f"&container={container}&videoCodec={video_codec}&audioCodec={audio_codec}&replace=true)"
        f"+add-limitation(scope=videoCodec&scopeName={video_codec}&type=upperBound"
        f"&name=video.width&value={width})"
        f"+add-limitation(scope=videoCodec&scopeName={video_codec}&type=upperBound"
        f"&name=video.height&value={height})")


LOCAL_ADMIN_TOKEN = None  # set from --token-file in main; header-only, never logged/profile/argv


def plex_headers(profile) -> dict:
    """Identity sent BOTH as query params and as HTTP headers."""
    headers = {
        "X-Plex-Platform": profile.platform,
        "X-Plex-Client-Identifier": profile.client_identifier,
        "X-Plex-Product": "EtherLabBench",
        "X-Plex-Version": "1.0",
        "X-Plex-Device": "Linux",
        "X-Plex-Provides": "player",
        "X-Plex-Client-Profile-Extra": profile_extra(
            profile.video_codec, profile.audio_codec, profile.video_resolution,
            protocol=profile.protocol),
    }
    if LOCAL_ADMIN_TOKEN:
        headers["X-Plex-Token"] = LOCAL_ADMIN_TOKEN
    return headers


def build_process_tree(ppid_map: dict, root_pid: int) -> list:
    """All descendants of root_pid (inclusive) from a pid->ppid map."""
    children = {}
    for pid, ppid in ppid_map.items():
        children.setdefault(ppid, []).append(pid)
    out, stack, seen = [], [root_pid], set()
    while stack:
        pid = stack.pop()
        if pid in seen:
            continue
        seen.add(pid)
        out.append(pid)
        stack.extend(children.get(pid, []))
    return out


# ---------- Windows process sampling (ctypes, stdlib) ----------
def _snapshot():
    """Return (ppid_map, name_map) via Toolhelp32 snapshot."""
    class PE32(ctypes.Structure):
        _fields_ = [("dwSize", ctypes.c_ulong), ("cntUsage", ctypes.c_ulong),
                    ("th32ProcessID", ctypes.c_ulong), ("th32DefaultHeapID", ctypes.c_void_p),
                    ("th32ModuleID", ctypes.c_ulong), ("cntThreads", ctypes.c_ulong),
                    ("th32ParentProcessID", ctypes.c_ulong), ("pcPriClassBase", ctypes.c_long),
                    ("dwFlags", ctypes.c_ulong), ("szExeFile", ctypes.c_wchar * 260)]
    k32 = ctypes.windll.kernel32
    snap = k32.CreateToolhelp32Snapshot(0x2, 0)  # TH32CS_SNAPPROCESS
    if snap == -1:
        raise pb.BenchError("CreateToolhelp32Snapshot failed")
    ppid_map, name_map = {}, {}
    try:
        entry = PE32()
        entry.dwSize = ctypes.sizeof(PE32)
        ok = k32.Process32FirstW(snap, ctypes.byref(entry))
        while ok:
            ppid_map[int(entry.th32ProcessID)] = int(entry.th32ParentProcessID)
            name_map[int(entry.th32ProcessID)] = entry.szExeFile
            ok = k32.Process32NextW(snap, ctypes.byref(entry))
    finally:
        k32.CloseHandle(snap)
    return ppid_map, name_map


def _proc_cpu_rss(pid: int):
    k32 = ctypes.windll.kernel32
    handle = k32.OpenProcess(0x1000, False, pid)  # QUERY_LIMITED_INFORMATION
    if not handle:
        return 0.0, 0
    try:
        class FT(ctypes.Structure):
            _fields_ = [("lo", ctypes.c_ulong), ("hi", ctypes.c_ulong)]
            def seconds(self): return ((self.hi << 32) | self.lo) / 1e7
        c, e, kern, user = FT(), FT(), FT(), FT()
        cpu = 0.0
        if k32.GetProcessTimes(handle, ctypes.byref(c), ctypes.byref(e),
                               ctypes.byref(kern), ctypes.byref(user)):
            cpu = kern.seconds() + user.seconds()
        rss = 0
        class PMC(ctypes.Structure):
            _fields_ = [("cb", ctypes.c_ulong), ("PageFaultCount", ctypes.c_ulong),
                        ("PeakWorkingSetSize", ctypes.c_size_t), ("WorkingSetSize", ctypes.c_size_t),
                        ("q1", ctypes.c_size_t), ("q2", ctypes.c_size_t), ("q3", ctypes.c_size_t),
                        ("q4", ctypes.c_size_t), ("q5", ctypes.c_size_t), ("q6", ctypes.c_size_t)]
        counters = PMC()
        counters.cb = ctypes.sizeof(PMC)
        try:
            if ctypes.windll.psapi.GetProcessMemoryInfo(handle, ctypes.byref(counters), counters.cb):
                rss = int(counters.WorkingSetSize)
        except Exception:
            pass
        return cpu, rss
    finally:
        k32.CloseHandle(handle)


def sample_tree_win(root_pid: int) -> dict:
    ppid_map, name_map = _snapshot()
    total_cpu, total_rss, procs = 0.0, 0, []
    for pid in build_process_tree(ppid_map, root_pid):
        cpu, rss = _proc_cpu_rss(pid)
        total_cpu += cpu
        total_rss += rss
        procs.append({"pid": pid, "comm": name_map.get(pid, ""), "cpu_seconds": round(cpu, 4),
                      "rss_bytes": rss, "read_bytes": 0, "write_bytes": 0})
    return {"cpu_seconds": total_cpu, "rss_bytes": total_rss, "read_bytes": 0,
            "write_bytes": 0, "processes": procs}


def capture_transcoder_cmdlines_win(root_pid: int) -> list:
    ppid_map, name_map = _snapshot()
    pids = [p for p in build_process_tree(ppid_map, root_pid)
            if "Transcoder" in name_map.get(p, "")]
    out = []
    for pid in pids:
        cmdline = ""
        try:
            r = subprocess.run(
                ["powershell", "-NoProfile", "-Command",
                 f"(Get-CimInstance Win32_Process -Filter 'ProcessId={pid}').CommandLine"],
                capture_output=True, text=True, timeout=15)
            cmdline = r.stdout.strip()
        except Exception:
            pass
        out.append({"pid": pid, "argv": cmdline.split() if cmdline else [],
                    "cmdline": pb.sanitize(cmdline)})
    return out


def compute_tree_delta(start, end) -> dict:
    """Honest CPU delta (ba985258 mirror): invalid/incomplete -> nulls, never 0/0."""
    unavailable = {"tree_cpu_delta_s": None, "tree_io_delta": None,
                   "cpu_sampling": "unavailable",
                   "cpu_sampling_reason": "invalid/incomplete tree delta (no root pid, "
                                          "no samples, or children exited dropping counters)"}
    if not start or not end or not end.get("processes"):
        return unavailable
    cpu = end.get("cpu_seconds", 0) - start.get("cpu_seconds", 0)
    read_b = end.get("read_bytes", 0) - start.get("read_bytes", 0)
    write_b = end.get("write_bytes", 0) - start.get("write_bytes", 0)
    if cpu < 0 or read_b < 0 or write_b < 0:
        return unavailable
    return {"tree_cpu_delta_s": round(cpu, 3),
            "tree_io_delta": {"read_bytes": read_b, "write_bytes": write_b},
            "cpu_sampling": "available", "cpu_sampling_reason": None}


def verify_output_win(cfg, out_dir: Path, sessions: list) -> dict:
    """Verify EVERY session (ba985258 mirror); missing segments = unchecked, not skipped."""
    ffprobe = cfg.ffprobe or str(Path(cfg.lab_root).parent / "ffmpeg" / "ffprobe.exe")
    entries = []
    for session in sorted(sessions, key=lambda s: s["session_index"]):
        idx = session["session_index"]
        entry = {"session_index": idx, "checked": False, "codec": None, "width": None,
                 "height": None, "matches_codec": False, "matches_resolution": False}
        seg_dir = out_dir / f"session-{idx}"
        segs = sorted(seg_dir.glob("seg-*.ts")) if seg_dir.exists() else []
        if segs and Path(ffprobe).exists():
            tmp = seg_dir / "concat.ts"
            tmp.write_bytes(b"".join(p.read_bytes() for p in segs))
            try:
                run = subprocess.run([ffprobe, "-v", "error", "-select_streams", "v:0",
                                      "-show_entries", "stream=codec_name,width,height",
                                      "-of", "json", str(tmp)],
                                     capture_output=True, text=True, timeout=30)
                if run.returncode == 0:
                    stream = (json.loads(run.stdout).get("streams") or [{}])[0]
                    entry.update({"checked": True, "codec": stream.get("codec_name"),
                                  "width": stream.get("width"), "height": stream.get("height"),
                                  "matches_codec": stream.get("codec_name") == cfg.profile.video_codec,
                                  "matches_resolution": f"{stream.get('width')}x{stream.get('height')}" == cfg.profile.video_resolution})
            except Exception:
                pass
        entries.append(entry)
    if not entries:
        return {"checked": False, "matches_codec": False, "matches_resolution": False, "sessions": []}
    first = entries[0]
    return {"checked": all(e["checked"] for e in entries),
            "matches_codec": all(e["matches_codec"] for e in entries),
            "matches_resolution": all(e["matches_resolution"] for e in entries),
            "sessions": entries, "codec": first["codec"], "width": first["width"],
            "height": first["height"], "expected_codec": cfg.profile.video_codec}


ACCEPTED_DECISION_CODES = {"1000", "1001"}


def check_decision_gate(root) -> dict:
    """Corrected gate (5a277350): accept 1000/1001 only; reject decision=none."""
    codes = {k: root.get(k) for k in ("generalDecisionCode", "transcodeDecisionCode")}
    present = [v for v in codes.values() if v is not None]
    if not present or any(v not in ACCEPTED_DECISION_CODES for v in present):
        raise pb.BenchError(f"decision rejected: codes={codes}")
    for elem in root.iter():
        if elem.tag in ("Media", "Part") and elem.get("decision") == "none":
            raise pb.BenchError(f"decision rejected: <{elem.tag} decision=none>")
    return codes


def run_session_win(cfg, root_pid, out_dir: Path, idx: int) -> dict:
    """pb.run_session flow with Android profile headers + decision gate."""
    session = uuid.uuid4().hex
    # PMS kills the previous job sharing an X-Plex-Client-Identifier even with
    # a different session GUID: every session needs its own client identifier.
    prof = dataclasses.replace(cfg.profile,
                               client_identifier=f"{cfg.profile.client_identifier}-{session}")
    headers = plex_headers(prof)
    params = prof.decision_params(cfg.rating_key, session)
    params.update({k: v for k, v in headers.items() if k != "X-Plex-Token"})  # token: header only
    qs = urllib.parse.urlencode(params)
    result = {"session_index": idx, "session_id": session, "profile": asdict(prof),
              "requested_duration_s": cfg.duration_s, "mode": cfg.mode}
    stop_url = (f"{cfg.base_url}/video/:/transcode/universal/stop?"
                f"{urllib.parse.urlencode({'session': session})}")
    t_setup = time.monotonic()
    try:
        status, _, body = pb.http_get(
            f"{cfg.base_url}/video/:/transcode/universal/decision?{qs}",
            timeout=min(30, cfg.timeout_s), headers=headers)
        if status != 200:
            raise pb.BenchError(f"decision HTTP {status}")
        result["decision_xml_sanitized"] = pb.sanitize(body.decode(errors="replace"))[:8000]
        try:
            root = ET.fromstring(body)
        except ET.ParseError as exc:
            raise pb.BenchError(f"decision XML parse failed: {exc}") from exc
        result["decision_codes"] = check_decision_gate(root)
        result["decision"] = {k: pb.sanitize(v) for k, v in root.attrib.items()}
        result["decision_media"] = [
            {k: pb.sanitize(v) for k, v in m.attrib.items()} for m in root.iter("Media")]
        result["setup_s"] = time.monotonic() - t_setup

        t_enc = time.monotonic()
        first_latency = None
        start_url = f"{cfg.base_url}/video/:/transcode/universal/start.m3u8?{qs}"
        status, _, body = pb.http_get(start_url, timeout=min(30, cfg.timeout_s), headers=headers)
        if status != 200:
            raise pb.BenchError(f"start.m3u8 HTTP {status}")
        is_master, media_url, segs, total, end = pb.parse_playlist(
            body.decode(errors="replace"), start_url)
        if is_master:
            status, _, body = pb.http_get(media_url, timeout=min(30, cfg.timeout_s), headers=headers)
            if status != 200:
                raise pb.BenchError(f"media playlist HTTP {status}")
            _, _, segs, total, end = pb.parse_playlist(body.decode(errors="replace"), media_url)
            playlist_url = media_url
        else:
            playlist_url = start_url

        downloaded, seen = {}, set()
        ordinals = {}  # uri -> stable playlist ordinal
        peak_rss = 0
        cpu_start = sample_tree_win(root_pid) if root_pid else None
        samples, cmdlines = [], []
        deadline = t_enc + cfg.timeout_s
        media_dir = out_dir / f"session-{idx}"
        if cfg.preserve_media:
            media_dir.mkdir(parents=True, exist_ok=True)
        lock = threading.Lock()
        argv_stop = threading.Event()

        def argv_poller():
            while not argv_stop.is_set():
                if root_pid:
                    found = capture_transcoder_cmdlines_win(root_pid)
                    if found:
                        with lock:
                            cmdlines[:] = found
                argv_stop.wait(0.05)  # poll argv every 50ms from encode start

        poller = threading.Thread(target=argv_poller, daemon=True)
        poller.start()

        def fetch(item):
            nonlocal first_latency
            uri, extinf = item
            st, _, data = pb.http_get(uri, timeout=min(30, cfg.timeout_s), headers=headers)
            if st != 200:
                raise pb.BenchError(f"segment HTTP {st}")
            if not data:
                raise pb.BenchError(f"empty segment {pb.sanitize(uri)}")
            with lock:
                downloaded[uri] = (len(data), extinf)
                ordinal = ordinals.get(uri, 0)
                if ordinal == 0 and first_latency is None:
                    first_latency = time.monotonic() - t_enc  # segment-0 body fully downloaded
            if cfg.preserve_media:
                (media_dir / f"seg-{ordinal:05d}.ts").write_bytes(data)
            return len(data)

        while time.monotonic() < deadline:
            try:
                status, _, body = pb.http_get(playlist_url, timeout=min(15, cfg.timeout_s),
                                              headers=headers)
                if status != 200:
                    raise pb.BenchError(f"playlist HTTP {status}")
                _, _, segs, total, end = pb.parse_playlist(body.decode(errors="replace"), playlist_url)
            except pb.BenchError:
                if downloaded:
                    raise
                time.sleep(cfg.poll_interval_s)
                continue
            for ordinal, (uri, _ext) in enumerate(segs):
                ordinals.setdefault(uri, ordinal)
            queued_s = sum(v[1] for v in downloaded.values())
            new = []
            for item in segs:  # queue capped at requested duration
                if item[0] in seen:
                    continue
                if queued_s >= cfg.duration_s:
                    break
                seen.add(item[0])
                new.append(item)
                queued_s += item[1]
            if new:
                if cfg.mode == "paced":
                    for item in new:
                        fetch(item)
                        time.sleep(min(item[1], 2.0))
                else:
                    with concurrent.futures.ThreadPoolExecutor(max_workers=min(8, len(new))) as ex:
                        list(ex.map(fetch, new))
            if root_pid:
                smp = sample_tree_win(root_pid)
                samples.append(smp)
                peak_rss = max(peak_rss, smp["rss_bytes"])
            actual_media = sum(v[1] for v in downloaded.values())
            if actual_media >= cfg.duration_s and end:
                break
            if end and not new and downloaded:
                break
            time.sleep(cfg.poll_interval_s)
        else:
            raise pb.BenchError(f"session timeout after {cfg.timeout_s}s")
        argv_stop.set()
        poller.join(timeout=2)
        if root_pid and not cmdlines:
            raise pb.BenchError("no transcoder argv captured from owned PMS tree; "
                                "rejecting as non-authentic result")
        actual_media = sum(v[1] for v in downloaded.values())
        if not downloaded:
            raise pb.BenchError("no segments downloaded")
        if actual_media <= 0:
            raise pb.BenchError("unknown media duration: EXTINF total is zero")
        result.update({"encode_s": time.monotonic() - t_enc,
                       "first_segment_latency_s": first_latency,
                       "actual_media_s": round(actual_media, 3),
                       "segment_count": len(downloaded),
                       "output_bytes": sum(v[0] for v in downloaded.values()),
                       "peak_rss_bytes": peak_rss, "samples": samples[-20:],
                       "transcoder_cmdlines": cmdlines, "playlist_endlist": end})
        cpu_end = sample_tree_win(root_pid) if root_pid else None
        result.update(compute_tree_delta(cpu_start, cpu_end))
        actual_codec = None
        for media in result.get("decision_media", []):
            actual_codec = media.get("videoCodec") or actual_codec
        result["requested_vs_actual"] = {
            "requested_video_codec": prof.video_codec, "actual_video_codec": actual_codec,
            "requested_resolution": prof.video_resolution,
            "actual_resolution": (result["decision_media"][0].get("videoResolution")
                                  if result.get("decision_media") else None),
            "hw_note": "inspect transcoder cmdline for nvenc tokens (native PMS NVENC expected)",
            "transcoder_used_hw": any("nvenc" in c["cmdline"] for c in cmdlines)}
        return result
    finally:
        try:
            argv_stop.set()
        except Exception:
            pass
        stop_status = None
        try:
            stop_status, _, _ = pb.http_get(stop_url, timeout=5, headers=headers)
        except Exception:
            stop_status = None
        result["stop_status"] = stop_status
        result["stopped_session"] = stop_status in (200, 204)


def read_local_app_data_path():
    import winreg
    try:
        with winreg.OpenKey(winreg.HKEY_CURRENT_USER, REG_KEY) as key:
            value, _ = winreg.QueryValueEx(key, REG_VALUE)
            return value
    except OSError:
        return None


class PmsProcessWin:
    def __init__(self, cfg):
        self.cfg = cfg
        self.proc = None
        self.pid = None

    def start(self):
        plan = isolation_plan(self.cfg.lab_root)
        live = read_local_app_data_path()
        if not live or os.path.normcase(live.rstrip("\\")) != os.path.normcase(plan["local_app_data_path"]):
            raise pb.BenchError(
                f"registry {REG_VALUE}={live!r} != planned {plan['local_app_data_path']!r}; "
                "run the lab setup script first (refusing to launch: would write to C:)")
        exe = Path(plan["pms_exe"])
        if not exe.exists():
            raise pb.BenchError(f"PMS binary missing: {exe}")
        env = dict(os.environ)
        env["TMP"] = env["TEMP"] = plan["transcode_dir"]
        self.proc = subprocess.Popen(
            [str(exe)], cwd=str(exe.parent), env=env,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)
        self.pid = self.proc.pid
        self.job = self._assign_job()
        return self.pid

    def wait_identity(self):
        deadline = time.monotonic() + self.cfg.startup_timeout_s
        last = ""
        while time.monotonic() < deadline:
            if self.proc and self.proc.poll() is not None:
                raise pb.BenchError(f"PMS exited early rc={self.proc.returncode}")
            try:
                status, _, body = pb.http_get(self.cfg.base_url + "/identity", timeout=5)
                if status == 200:
                    root = ET.fromstring(body)
                    return {"claimed": root.get("claimed"), "version": root.get("version"),
                            "machineIdentifier_present": bool(root.get("machineIdentifier"))}
            except Exception as exc:  # noqa: BLE001 - retried until deadline
                last = str(exc)
            time.sleep(0.5)
        raise pb.BenchError(f"PMS identity not ready in {self.cfg.startup_timeout_s}s: {last}")

    def _assign_job(self):
        """Job Object with KILL_ON_JOB_CLOSE: closing it kills the whole tree,
        including reparented children that taskkill /T would leak."""
        k32 = ctypes.windll.kernel32

        class JOBOBJECT_BASIC_LIMIT_INFORMATION(ctypes.Structure):
            _fields_ = [("PerProcessUserTimeLimit", ctypes.c_int64),
                        ("PerJobUserTimeLimit", ctypes.c_int64),
                        ("LimitFlags", ctypes.c_ulong), ("MinimumWorkingSetSize", ctypes.c_size_t),
                        ("MaximumWorkingSetSize", ctypes.c_size_t), ("ActiveProcessLimit", ctypes.c_ulong),
                        ("Affinity", ctypes.c_size_t), ("PriorityClass", ctypes.c_ulong),
                        ("SchedulingClass", ctypes.c_ulong)]

        class IO_COUNTERS(ctypes.Structure):
            _fields_ = [(n, ctypes.c_uint64) for n in (
                "ReadOperationCount", "WriteOperationCount", "OtherOperationCount",
                "ReadTransferCount", "WriteTransferCount", "OtherTransferCount")]

        class JOBOBJECT_EXTENDED_LIMIT_INFORMATION(ctypes.Structure):
            _fields_ = [("BasicLimitInformation", JOBOBJECT_BASIC_LIMIT_INFORMATION),
                        ("IoInfo", IO_COUNTERS), ("ProcessMemoryLimit", ctypes.c_size_t),
                        ("JobMemoryLimit", ctypes.c_size_t), ("PeakProcessMemoryUsed", ctypes.c_size_t),
                        ("PeakJobMemoryUsed", ctypes.c_size_t)]

        job = k32.CreateJobObjectW(None, None)
        if not job:
            return None
        info = JOBOBJECT_EXTENDED_LIMIT_INFORMATION()
        info.BasicLimitInformation.LimitFlags = 0x2000  # JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        if not k32.SetInformationJobObject(job, 9, ctypes.byref(info), ctypes.sizeof(info)):
            k32.CloseHandle(job)
            return None
        if not k32.AssignProcessToJobObject(job, int(self.proc._handle)):
            k32.CloseHandle(job)
            return None
        return job

    def stop(self):
        if not self.proc:
            return
        descendants = []
        try:  # capture descendants BEFORE exit as a fallback audit/leak check
            ppid_map, _ = _snapshot()
            descendants = build_process_tree(ppid_map, self.pid)
        except Exception:
            pass
        try:
            if getattr(self, "job", None):
                ctypes.windll.kernel32.CloseHandle(self.job)  # KILL_ON_JOB_CLOSE
                self.job = None
            if self.proc.poll() is None:
                self.proc.terminate()
            try:
                self.proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                for pid in descendants:  # job unavailable/failed: kill captured tree
                    subprocess.run(["taskkill", "/PID", str(pid), "/F"],
                                   capture_output=True, timeout=10)
                self.proc.wait(timeout=10)
        except Exception:
            pass


def main(argv=None):
    ap = argparse.ArgumentParser(description="Authentic native Windows Plex NVENC baseline")
    ap.add_argument("--lab-root", default=DEFAULT_LAB)
    ap.add_argument("--base-url", default="http://127.0.0.1:32400")
    ap.add_argument("--rating-key", default="1")
    ap.add_argument("--duration", type=float, default=10.0)
    ap.add_argument("--mode", choices=["fast", "paced"], default="fast")
    ap.add_argument("--concurrency", type=int, default=1)
    ap.add_argument("--repeats", type=int, default=1)
    ap.add_argument("--timeout", type=float, default=120.0)
    ap.add_argument("--no-launch", action="store_true")
    ap.add_argument("--preserve-media", action="store_true")
    ap.add_argument("--out", default="")
    ap.add_argument("--ffprobe", default="")
    ap.add_argument("--video-codec", default="h264")
    ap.add_argument("--video-resolution", default="640x360")
    ap.add_argument("--max-video-bitrate", type=int, default=2000)
    ap.add_argument("--audio-codec", default="aac")
    ap.add_argument("--print-isolation-plan", action="store_true")
    ap.add_argument("--token-file", default="",
                    help="LocalAdminToken file; contents sent as X-Plex-Token header only")
    args = ap.parse_args(argv)
    plan = isolation_plan(args.lab_root)
    if args.print_isolation_plan:
        print(json.dumps(plan, indent=2))
        return 0
    if sys.platform != "win32":
        print(json.dumps({"ok": False, "error": "native Windows driver: run on gpu-workstation"}))
        return 2
    token_file = Path(args.token_file) if args.token_file else (
        Path(plan["expected_data_dir"]) / ".LocalAdminToken")

    def load_token():
        # PMS rotates .LocalAdminToken on each start: (re)read after launch.
        if token_file.exists():
            globals()["LOCAL_ADMIN_TOKEN"] = token_file.read_text().strip() or None

    load_token()
    # Rebind the Linux samplers in plex_bench to the Windows ones.
    pb.sample_tree = sample_tree_win
    pb.capture_transcoder_cmdlines = capture_transcoder_cmdlines_win
    out_dir = Path(args.out) if args.out else Path(plan["results_dir"]) / f"run-{int(time.time())}"
    out_dir.mkdir(parents=True, exist_ok=True)
    cfg = pb.Config(lab_root=args.lab_root, base_url=args.base_url, rating_key=args.rating_key,
                    duration_s=args.duration, mode=args.mode, concurrency=args.concurrency,
                    repeats=args.repeats, timeout_s=args.timeout, launch=not args.no_launch,
                    preserve_media=args.preserve_media, out_dir=str(out_dir), ffprobe=args.ffprobe,
                    profile=pb.Profile(video_codec=args.video_codec,
                                       video_resolution=args.video_resolution,
                                       max_video_bitrate_kbps=args.max_video_bitrate,
                                       audio_codec=args.audio_codec, platform="Android"))
    fixture = Path(plan["fixture"])
    meta = {"tool": "plex_bench_win/1", "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "config": {**asdict(cfg), "profile": asdict(cfg.profile)}, "isolation_plan": plan,
            "source": {"fixture": str(fixture),
                       "sha256": pb.sha256_file(fixture) if fixture.exists() else None,
                       "bytes": fixture.stat().st_size if fixture.exists() else None},
            "host_label": "Native Windows, RTX 4080 (NVENC), PMS as owned child, registry-isolated data dir"}
    pms, root_pid, sessions = None, None, []
    try:
        if cfg.launch:
            pms = PmsProcessWin(cfg)
            meta["pms_pid"] = pms.start()
            root_pid = pms.pid
            meta["identity"] = pms.wait_identity()
            load_token()
        else:
            status, _, body = pb.http_get(cfg.base_url + "/identity", timeout=10)
            root = ET.fromstring(body)
            meta["identity"] = {"claimed": root.get("claimed"), "version": root.get("version")}
        meta["pms_version"] = meta.get("identity", {}).get("version")
        waves = []
        for wave in range(cfg.repeats):
            wave_start = time.monotonic()  # setup-inclusive wave wall
            wave_sessions = []
            with concurrent.futures.ThreadPoolExecutor(max_workers=cfg.concurrency) as ex:
                futs = [ex.submit(run_session_win, cfg, root_pid, out_dir,
                                  wave * cfg.concurrency + i) for i in range(cfg.concurrency)]
                for fut in futs:
                    wave_sessions.append(fut.result())
            wall_s = round(time.monotonic() - wave_start, 3)
            media_total = round(sum(s["actual_media_s"] for s in wave_sessions), 3)
            waves.append({"wave": wave, "wall_s": wall_s, "concurrency": cfg.concurrency,
                          "sessions": len(wave_sessions), "actual_media_total_s": media_total,
                          "aggregate_media_per_wall_s": round(media_total / wall_s, 3) if wall_s else None})
            sessions.extend(wave_sessions)
        meta["waves"] = waves
        meta["sessions"] = sessions
        meta["verify"] = verify_output_win(cfg, out_dir, sessions)
        if cfg.preserve_media and sessions and not meta["verify"]["checked"]:
            raise pb.BenchError("verify mismatch: not all sessions checked")
        if meta["verify"]["checked"] and not (meta["verify"]["matches_codec"]
                                              and meta["verify"]["matches_resolution"]):
            raise pb.BenchError("verify mismatch: codec/resolution does not match profile")
        meta["ok"] = True
    except Exception as exc:  # noqa: BLE001 - recorded sanitized in result.json
        meta["ok"] = False
        meta["error"] = pb.sanitize(str(exc))
        meta["sessions"] = sessions
        (out_dir / "result.json").write_text(json.dumps(meta, indent=2))
        print(json.dumps({"ok": False, "error": meta["error"], "out": str(out_dir)}))
        return 1
    finally:
        if pms:
            pms.stop()
            meta["pms_stopped"] = True
    (out_dir / "result.json").write_text(json.dumps(meta, indent=2))
    print(json.dumps({"ok": True, "out": str(out_dir), "sessions": len(sessions),
                      "actual_media_s": [s.get("actual_media_s") for s in sessions]}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
