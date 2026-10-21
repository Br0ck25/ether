#!/usr/bin/env python3
"""Native macOS Plex transcode comparison driver (stdlib only).

Reuses the authentic HTTP driver in plex_bench.py (decision + HLS start,
EXTINF accounting, segment download, output verification semantics) and adds
only the macOS-specific pieces:

  * launch of the official .app binary as an owned process group,
  * process-tree CPU/RSS sampling via /bin/ps (no /proc on macOS),
  * capture of the authentic `Plex Transcoder` argv from owned descendants,
  * an isolated Application Support / CFPreferences strategy that never
    touches pre-existing user files and never repurposes HOME,
  * a hard launch gate: PMS is never started (and port 32400 never bound by
    us) unless --window-granted is passed, which the operator must only do
    after the runtime owner has stopped Ether and the root has granted the
    coordinated window.

Fields macOS cannot supply (per-process cumulative read/write bytes) are
reported as explicit nulls, never fake zeros.
"""
from __future__ import annotations
import argparse, concurrent.futures, json, os, re, signal, socket, subprocess, sys, threading, time, uuid
import xml.etree.ElementTree as ET
from dataclasses import asdict, dataclass, field
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import plex_bench as pb

BenchError = pb.BenchError
DEFAULT_APP = "/Users/braedonsaunders/.bb/thread-storage/thr_dbguekh66b/transcode-lab/plex-macos/Plex Media Server.app"
DEFAULT_LAB = "/Users/braedonsaunders/.bb/thread-storage/thr_dbguekh66b/transcode-lab/plex-macos-lab"
PREFS_DOMAIN = "com.plexapp.plexmediaserver"
HW_TOKENS = ("videotoolbox", "video_toolbox", "vaapi", "nvenc", "qsv")

# ---------------------------------------------------------------- plan ----
START_PLAN = {
    "preconditions": [
        "root (thr_dbguekh66b) explicitly grants the window in this thread",
        "runtime owner (thr_gfsnk4z9fi) confirms Ether stopped; nothing listening on 32400",
        "driver preflight passes: default Plex data path and prefs plist absent (or our own symlink), fixture present, port 32400 not connectable",
    ],
    "window_5min_timeline": [
        "T+0:00  python3 plex_bench_mac.py --preflight-only  (read-only checks, no launch)",
        "T+0:30  launch ONE command: --prepare-hold --window-granted --window-deadline-s 300 [--token-file <private>]; PMS starts in its own pgid, driver waits /identity then publishes <lab>/ready.json (pid/pgid/run_id/deadline) and HOLDS -- it does not encode yet",
        "T+1:30  root privately claims server with Plex Pass account (credentials never enter driver except optional --token-file, whose content is never logged)",
        "T+2:30  root creates/scans 'Lab' library, then writes <lab>/continue.json {\"action\":\"continue\",\"rating_key\":\"<key>\",\"duration_s\":10} (or {\"action\":\"abort\"}); stale/wrong-run_id signals are rejected",
        "T+3:30  driver runs tiny smoke for that ratingKey only, timeout capped at deadline minus 30s stop reserve: source.mkv 60s 640x360, 10s, Android h264 640x360@2000k; require videotoolbox in captured argv (else software fallback)",
        "T+4:30  driver stops session, SIGTERM owned pgid (SIGKILL after 10s), restores original absence of data path/prefs, verifies no owned processes and port 32400 free",
        "T+5:00  window ends; result.json + PMS log path reported to root; Ether may be restarted by its owner only after this report",
    ],
    "later_windows": [
        "generate exact 32s fixture (concat 2x16s sdr1080, -t 32, ffprobe-verified) only inside a granted resource window",
        "matched 32s runs: sdr1080_32s.mp4, source.mkv (60s), FourPeople 720p60 webm, same binary/fixtures for Ether comparison",
        "concurrency/repeat waves with wave wall timing; verify every session's codec/resolution",
    ],
    "abort_conditions": [
        "port 32400 connectable before launch", "pre-existing ~/Library Plex data or prefs plist",
        "PMS exits early or /identity not ready in cap", "no authentic Plex Transcoder argv captured",
        "any owned process survives stop (report pids; never kill processes we did not start)",
    ],
}

# ------------------------------------------------------- ps tree metrics --
def parse_ps_time(text: str) -> float:
    """ps TIME is [[dd-]hh:]mm:ss[.ss] cumulative CPU."""
    text = text.strip()
    days = 0.0
    if "-" in text:
        d, text = text.split("-", 1); days = float(d) * 86400.0
    parts = text.split(":")
    secs = float(parts[-1]); mins = float(parts[-2]) if len(parts) >= 2 else 0.0
    hours = float(parts[-3]) if len(parts) >= 3 else 0.0
    return days + hours * 3600.0 + mins * 60.0 + secs

def ps_snapshot() -> list[dict]:
    """One /bin/ps snapshot: pid, ppid, pgid, rss_bytes, cpu_seconds, args."""
    out = subprocess.run(["/bin/ps", "-axo", "pid=,ppid=,pgid=,rss=,time=,args="],
                         capture_output=True, text=True, timeout=15)
    if out.returncode != 0:
        raise BenchError(f"ps failed: {out.stderr[:300]}")
    rows = []
    for line in out.stdout.splitlines():
        m = re.match(r"\s*(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s+(\S+)\s+(.*)$", line)
        if not m:
            continue
        pid, ppid, pgid, rss_kb, cputime, args = m.groups()
        try:
            cpu = parse_ps_time(cputime)
        except ValueError:
            continue
        rows.append({"pid": int(pid), "ppid": int(ppid), "pgid": int(pgid),
                     "rss_bytes": int(rss_kb) * 1024, "cpu_seconds": cpu, "args": args})
    return rows

def tree_pids(rows: list[dict], root_pid: int) -> list[int]:
    children = {}
    for r in rows:
        children.setdefault(r["ppid"], []).append(r["pid"])
    out, stack, seen = [], [root_pid], set()
    while stack:
        p = stack.pop()
        if p in seen:
            continue
        seen.add(p); out.append(p); stack.extend(children.get(p, []))
    return out

def sample_tree(root_pid: int, rows: list[dict] | None = None) -> dict:
    rows = rows if rows is not None else ps_snapshot()
    pids = set(tree_pids(rows, root_pid))
    procs = [{"pid": r["pid"], "cpu_seconds": r["cpu_seconds"], "rss_bytes": r["rss_bytes"],
              "args": pb.sanitize(r["args"])[:300]} for r in rows if r["pid"] in pids]
    return {"cpu_seconds": round(sum(p["cpu_seconds"] for p in procs), 3),
            "rss_bytes": sum(p["rss_bytes"] for p in procs),
            # macOS ps exposes no per-process cumulative disk bytes: explicit nulls.
            "read_bytes": None, "write_bytes": None, "io_sampling": "unavailable",
            "processes": procs}

def capture_transcoder_cmdlines(root_pid: int, rows: list[dict] | None = None) -> list[dict]:
    rows = rows if rows is not None else ps_snapshot()
    pids = set(tree_pids(rows, root_pid))
    out = []
    for r in rows:
        if r["pid"] in pids and "Plex Transcoder" in r["args"]:
            out.append({"pid": r["pid"], "argv": [pb.sanitize(a) for a in r["args"].split()],
                        "cmdline": pb.sanitize(r["args"])})
    return out

def compute_tree_delta(start: dict, end: dict):
    """(cpu_delta_s, io_delta_or_None, valid). Negative/incomplete -> invalid."""
    try:
        cpu = round(end["cpu_seconds"] - start["cpu_seconds"], 3)
    except Exception:
        return None, None, False
    if cpu < 0 or not end.get("processes"):
        return None, None, False
    io = None
    if start.get("read_bytes") is not None and end.get("read_bytes") is not None:
        io = {"read_bytes": end["read_bytes"] - start["read_bytes"],
              "write_bytes": end["write_bytes"] - start["write_bytes"]}
        if io["read_bytes"] < 0 or io["write_bytes"] < 0:
            return None, None, False
    return cpu, io, True

# ------------------------------------------------------------- isolation --
class MacIsolation:
    """Isolated Plex data without repurposing HOME.

    PMS (macOS) derives its data dir from ~/Library/Application Support/
    Plex Media Server and its bundle id com.plexapp.plexmediaserver for
    CFPreferences. The binary also contains PLEX_MEDIA_SERVER_APPLICATION_
    SUPPORT_DIR, but whether the Mac build honors it is unverified until a
    granted window runs it, so the env var is only the primary hint: the
    load-bearing guard is a symlink at the default path, created only when
    that path is absent, pointing at the lab, and removed on stop -- which
    restores the original absence either way.

    Refuses to run if the default data path or the prefs plist pre-exists:
    pre-existing user files are never touched.
    """
    def __init__(self, lab_root: Path, home: Path | None = None):
        self.lab = Path(lab_root)
        self.home = Path(home) if home else Path.home()
        self.default_support = self.home / "Library" / "Application Support" / "Plex Media Server"
        self.prefs_plist = self.home / "Library" / "Preferences" / f"{PREFS_DOMAIN}.plist"
        self.lab_support = self.lab / "support"
        self.lab_tmp = self.lab / "tmp"
        self.lab_media = self.lab / "media"
        self.created_symlink = False
        self.prefs_existed_before = False

    def check_preexisting(self) -> dict:
        support_state = ("symlink->ours" if self.default_support.is_symlink()
                         and self.default_support.resolve() == self.lab_support.resolve()
                         else "present" if os.path.lexists(self.default_support) else "absent")
        prefs_state = "present" if os.path.lexists(self.prefs_plist) else "absent"
        return {"default_support": support_state, "prefs_plist": prefs_state,
                "home": str(self.home)}

    def prepare(self) -> dict:
        state = self.check_preexisting()
        if state["default_support"] == "present":
            raise BenchError(f"pre-existing Plex data at {self.default_support}; refusing to touch it")
        if state["prefs_plist"] == "present":
            raise BenchError(f"pre-existing prefs {self.prefs_plist}; refusing to touch it")
        self.prefs_existed_before = False
        for d in (self.lab_support, self.lab_tmp, self.lab_media):
            d.mkdir(parents=True, exist_ok=True)
        if state["default_support"] == "absent":
            self.default_support.parent.mkdir(parents=True, exist_ok=True)
            os.symlink(self.lab_support, self.default_support)
            self.created_symlink = True
        return {"strategy": "env PLEX_MEDIA_SERVER_APPLICATION_SUPPORT_DIR (primary, Mac honoring unverified) "
                            "+ default-path symlink guard (load-bearing) + CFPreferences restore-absence",
                "env_support_dir": str(self.lab_support), "symlink_created": self.created_symlink,
                "prefs_domain": PREFS_DOMAIN, "home_repurposed": False, **state}

    def restore(self) -> dict:
        actions = []
        if self.created_symlink and self.default_support.is_symlink() \
                and self.default_support.resolve() == self.lab_support.resolve():
            self.default_support.unlink(); actions.append("removed lab symlink (default path absent again)")
        # CFPreferences: remove only what this run created (plist was absent before).
        if not self.prefs_existed_before and os.path.lexists(self.prefs_plist):
            subprocess.run(["/usr/bin/defaults", "delete", PREFS_DOMAIN],
                           capture_output=True, timeout=15)
            if os.path.lexists(self.prefs_plist):
                Path(self.prefs_plist).unlink()
            actions.append("removed CFPreferences domain/plist created by this run")
        return {"actions": actions, "after": self.check_preexisting()}

# -------------------------------------------------------------- PMS proc --
@dataclass
class MacConfig:
    app: str = DEFAULT_APP
    lab_root: str = DEFAULT_LAB
    fixture: str = ""
    base_url: str = "http://127.0.0.1:32400"
    rating_key: str = "1"
    duration_s: float = 32.0
    mode: str = "fast"
    concurrency: int = 1
    repeats: int = 1
    timeout_s: float = 120.0
    startup_timeout_s: float = 60.0
    preserve_media: bool = False
    window_granted: bool = False
    pms_pid: int | None = None  # external (already-running, owned by operator) tree to sample
    profile: pb.Profile = field(default_factory=pb.Profile)

    def pb_config(self) -> pb.Config:
        return pb.Config(lab_root=self.lab_root, base_url=self.base_url, rating_key=self.rating_key,
                         duration_s=self.duration_s, mode=self.mode, concurrency=self.concurrency,
                         repeats=self.repeats, timeout_s=self.timeout_s, launch=False,
                         preserve_media=self.preserve_media,
                         startup_timeout_s=self.startup_timeout_s, profile=self.profile)

def port_connectable(base_url: str) -> bool:
    hostport = base_url.split("://", 1)[-1].split("/", 1)[0]
    host, _, port = hostport.partition(":")
    try:
        with socket.create_connection((host or "127.0.0.1", int(port or 32400)), timeout=2):
            return True
    except OSError:
        return False

class PmsProcessMac:
    """Owns exactly one PMS process group; stop() signals only that pgid."""
    def __init__(self, cfg: MacConfig, isolation: MacIsolation):
        self.cfg = cfg; self.iso = isolation
        self.proc = None; self.pid = None; self.pgid = None; self.log_path = None

    def start(self) -> int:
        if sys.platform != "darwin":
            raise BenchError("native macOS driver only")
        if not self.cfg.window_granted:
            raise BenchError("refusing to start PMS: --window-granted not given "
                             "(root grant + Ether stopped on 32400 required)")
        binary = Path(self.cfg.app) / "Contents" / "MacOS" / "Plex Media Server"
        if not binary.exists():
            raise BenchError(f"PMS binary missing: {binary}")
        if port_connectable(self.cfg.base_url):
            raise BenchError(f"{self.cfg.base_url} already connectable; refusing to bind/compete for 32400")
        self.iso.prepare()
        env = dict(os.environ)  # HOME deliberately unchanged
        env.update({"PLEX_MEDIA_SERVER_APPLICATION_SUPPORT_DIR": str(self.iso.lab_support),
                    "PLEX_MEDIA_SERVER_TMPDIR": str(self.iso.lab_tmp),
                    "TMPDIR": str(self.iso.lab_tmp)})
        self.log_path = Path(self.cfg.lab_root) / "pms.log"
        log = open(self.log_path, "ab")
        self.proc = subprocess.Popen([str(binary)], cwd=str(self.iso.lab_support), env=env,
                                     stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        self.pid = self.proc.pid; self.pgid = os.getpgid(self.pid)
        return self.pid

    def wait_identity(self) -> dict:
        deadline = time.monotonic() + self.cfg.startup_timeout_s; last = ""
        while time.monotonic() < deadline:
            if self.proc and self.proc.poll() is not None:
                raise BenchError(f"PMS exited early rc={self.proc.returncode}; log={self.log_path}")
            try:
                s, _, b = pb.http_get(self.cfg.base_url + "/identity", timeout=5)
                if s == 200:
                    root = ET.fromstring(b)
                    return {"claimed": root.get("claimed"), "version": root.get("version"),
                            "machineIdentifier_present": bool(root.get("machineIdentifier"))}
            except Exception as e:
                last = str(e)
            time.sleep(0.5)
        raise BenchError(f"PMS identity not ready in {self.cfg.startup_timeout_s}s: {last}")

    def stop(self) -> dict:
        result = {"pgid": self.pgid, "signalled": False, "remaining_pids": []}
        if not self.proc:
            return result
        try:
            if self.proc.poll() is None:
                os.killpg(self.pgid, signal.SIGTERM); result["signalled"] = True
                try:
                    self.proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    os.killpg(self.pgid, signal.SIGKILL); self.proc.wait(timeout=10)
        except (ProcessLookupError, PermissionError):
            pass
        try:
            rows = ps_snapshot()
            result["remaining_pids"] = [r["pid"] for r in rows if r["pgid"] == self.pgid]
        except Exception:
            result["remaining_pids"] = []
        return result

# --------------------------------------------------------------- session --
def run_session_mac(cfg: MacConfig, root_pid: int | None, out_dir: Path, idx: int) -> dict:
    """pb.run_session does authentic HTTP; we sample/argv-capture around it."""
    pbcfg = cfg.pb_config()
    state = {"peak_rss": None, "argv": {}, "start": None, "end": None}
    stop_poll = threading.Event()
    def poller():
        if not root_pid:
            return
        state["start"] = sample_tree(root_pid)
        while not stop_poll.is_set():
            try:
                rows = ps_snapshot()
                smp = sample_tree(root_pid, rows)
                state["peak_rss"] = smp["rss_bytes"] if state["peak_rss"] is None else max(state["peak_rss"], smp["rss_bytes"])
                for c in capture_transcoder_cmdlines(root_pid, rows):
                    state["argv"][(c["pid"], c["cmdline"])] = c
            except Exception:
                pass
            stop_poll.wait(0.1)
    th = threading.Thread(target=poller, daemon=True); th.start()
    # Header auth on every HTTP request (incl. segments/playlist/stop, which
    # pb calls without profile headers). If an outer scope (prepare-hold /
    # _run_waves caller) already installed the wrapper, reuse it.
    _token = getattr(cfg.profile, "_token", "")
    _restore = (lambda: None) if getattr(pb.http_get, "_is_auth_wrapper", False) \
        else _install_auth_http(_token, cfg.base_url)
    try:
        result = pb.run_session(pbcfg, None, out_dir, idx)
    finally:
        _restore()
        stop_poll.set(); th.join(timeout=3)
    if root_pid:
        state["end"] = sample_tree(root_pid)
        cmdlines = list(state["argv"].values())
        if not cmdlines:
            raise BenchError("authentic encode claimed but no Plex Transcoder argv captured from owned descendants")
        result["transcoder_cmdlines"] = cmdlines
        result["peak_rss_bytes"] = state["peak_rss"]
        delta, io_delta, valid = compute_tree_delta(state["start"] or {}, state["end"])
        if valid:
            result["tree_cpu_delta_s"] = delta; result["tree_io_delta"] = io_delta
            result["cpu_sampling"] = "available (ps cumulative TIME; io bytes unavailable on macOS)"
        else:
            result["tree_cpu_delta_s"] = None; result["tree_io_delta"] = None
            result["cpu_sampling"] = "unavailable"
            result["cpu_sampling_reason"] = "invalid/incomplete tree delta (negative; children likely exited)"
        rva = result.get("requested_vs_actual") or {}
        rva["transcoder_used_hw"] = any(any(t in c["cmdline"].lower() for t in HW_TOKENS) for c in cmdlines)
        rva["hw_tokens_checked"] = list(HW_TOKENS)
        rva["hw_note"] = "macOS VideoToolbox expected via videotoolbox token in authentic argv; absence = software fallback, not HW"
        result["requested_vs_actual"] = rva
    else:
        result["transcoder_cmdlines"] = []
        result["peak_rss_bytes"] = None; result["tree_cpu_delta_s"] = None; result["tree_io_delta"] = None
        result["cpu_sampling"] = "unavailable (no owned pid)"
    return result

def verify_all(cfg: MacConfig, out_dir: Path, sessions: list) -> dict:
    """Every preserved session probed; aggregate requires all to match."""
    if not cfg.preserve_media:
        return {"checked": False, "reason": "preserve_media off"}
    ffprobe = "/opt/homebrew/bin/ffprobe" if Path("/opt/homebrew/bin/ffprobe").exists() else "ffprobe"
    per = []
    for idx, sess in enumerate(sessions):
        sidx = sess.get("session_index", idx) if isinstance(sess, dict) else idx
        d = out_dir / f"session-{sidx}"; segs = sorted(d.glob("seg-*.ts"))
        if not segs:
            per.append({"session_index": sidx, "checked": False, "matches_codec": False,
                        "matches_resolution": False, "reason": "no preserved segments"}); continue
        tmp = d / "concat.ts"; tmp.write_bytes(b"".join(p.read_bytes() for p in segs))
        try:
            r = subprocess.run([ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries",
                                "stream=codec_name,width,height", "-of", "json", str(tmp)],
                               capture_output=True, text=True, timeout=30)
            if r.returncode != 0:
                raise BenchError(f"ffprobe failed session {sidx}: {pb.sanitize(r.stderr[:300])}")
            st = (json.loads(r.stdout).get("streams") or [{}])[0]
            per.append({"session_index": sidx, "checked": True, "codec": st.get("codec_name"),
                        "width": st.get("width"), "height": st.get("height"),
                        "expected_codec": cfg.profile.video_codec,
                        "matches_codec": st.get("codec_name") == cfg.profile.video_codec,
                        "matches_resolution": f"{st.get('width')}x{st.get('height')}" == cfg.profile.video_resolution})
        except BenchError:
            raise
        except Exception as e:
            raise BenchError(f"ffprobe failed session {sidx}: {e}")
    if not per:
        return {"checked": False, "reason": "no sessions"}
    return {"checked": all(x["checked"] for x in per),
            "matches_codec": all(x.get("matches_codec") for x in per),
            "matches_resolution": all(x.get("matches_resolution") for x in per),
            "sessions": per}

# ------------------------------------------------------- prepare / hold --
class TokenProfile(pb.Profile):
    """pb.Profile plus an in-memory X-Plex-Token (never a dataclass field,
    so asdict()/result.json never contain it; pb.sanitize redacts it in text).
    The token is sent ONLY as a header (see _install_auth_http), never as a
    URL/query parameter and never inside profile JSON."""
    _token = ""
    def headers(self) -> dict:
        h = super().headers()
        if self._token:
            h["X-Plex-Token"] = self._token
        return h

_auth_lock = threading.Lock()
def _install_auth_http(token: str, base_url: str):
    """Wrap pb.http_get so EVERY driver request (decision, start, playlist,
    segments, stop, identity) carries X-Plex-Token as a header when the URL
    is under base_url. Returns a restore callable. No-op without a token."""
    if not token:
        return lambda: None
    original = pb.http_get
    def wrapper(url, timeout, headers=None):
        h = dict(headers or {})
        if url.startswith(base_url):
            h["X-Plex-Token"] = token
        return original(url, timeout, headers=h)
    wrapper._is_auth_wrapper = True  # type: ignore[attr-defined]
    with _auth_lock:
        pb.http_get = wrapper
    def restore():
        with _auth_lock:
            if getattr(pb.http_get, "_is_auth_wrapper", False):
                pb.http_get = original
    return restore

def load_token_file(path: str) -> str:
    """Read a private token file. Content is never logged or returned in metadata."""
    p = Path(path)
    if not p.exists():
        raise BenchError(f"token file missing: {p}")
    text = p.read_text(errors="strict").strip()
    token = text.splitlines()[0].strip() if text else ""
    if not token:
        raise BenchError("token file is empty")
    return token

def publish_ready(path: Path, payload: dict) -> None:
    """Atomically publish readiness/state JSON (tmp + rename)."""
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + f".tmp-{os.getpid()}")
    tmp.write_text(json.dumps(payload, indent=2))
    os.replace(tmp, path)

def read_signal(path: Path, run_id: str, not_before_mtime: float) -> dict | None:
    """Continue/abort signal written by root after claim/library setup.

    Only files written after readiness was published are honored, and a
    run_id, when present in the signal, must match. Returns None if absent.
    """
    try:
        st = path.stat()
    except OSError:
        return None
    if st.st_mtime < not_before_mtime - 1.0:
        return None  # stale signal from an earlier run
    try:
        data = json.loads(path.read_text())
    except Exception as e:
        raise BenchError(f"signal file is not valid JSON: {e}")
    if not isinstance(data, dict):
        raise BenchError("signal file must be a JSON object")
    if data.get("run_id") and data["run_id"] != run_id:
        raise BenchError("signal run_id does not match this run")
    action = data.get("action", "continue")
    if action not in ("continue", "abort"):
        raise BenchError(f"unknown signal action: {action!r}")
    data["action"] = action
    return data

def session_timeout_for(remaining_s: float, requested_timeout_s: float, stop_reserve_s: float) -> float:
    """Timeout that guarantees stop (incl. children) finishes inside the deadline."""
    budget = min(requested_timeout_s, remaining_s - stop_reserve_s)
    if budget <= 0:
        raise BenchError("window deadline leaves no budget for the benchmark (stop reserve protected)")
    return budget

def wait_for_signal(signal_path: Path, run_id: str, not_before_mtime: float,
                    deadline_monotonic: float, stop_reserve_s: float,
                    is_alive, on_poll=None, poll_interval_s: float = 0.25) -> dict:
    """Hold after identity-ready until root's signal, PMS death, or deadline."""
    while True:
        if not is_alive():
            raise BenchError("PMS exited while waiting for continue signal")
        sig = read_signal(signal_path, run_id, not_before_mtime)
        if sig is not None:
            return sig
        remaining = deadline_monotonic - time.monotonic()
        if remaining <= stop_reserve_s:
            raise BenchError(f"no continue signal before window deadline (stop reserve {stop_reserve_s}s protected)")
        if on_poll:
            on_poll(remaining)
        time.sleep(min(poll_interval_s, max(0.05, remaining - stop_reserve_s)))

def run_prepare_hold(cfg: MacConfig, out_dir: Path, ready_path: Path, signal_path: Path,
                     window_deadline_s: float = 300.0, stop_reserve_s: float = 30.0,
                     token: str = "", token_file: str = "", pms=None, run_bench=None) -> dict:
    """Launch -> identity -> publish readiness -> hold for root signal ->
    tiny benchmark for the signal's ratingKey -> always stop within deadline.

    `pms`/`run_bench` are injectable for offline tests; production passes a
    real PmsProcessMac (whose start() enforces --window-granted) and the
    wave runner. The token, if any, is used only in HTTP headers.
    """
    if window_deadline_s <= 0 or stop_reserve_s < 0 or stop_reserve_s >= window_deadline_s:
        raise BenchError("window deadline / stop reserve invalid")
    iso = MacIsolation(Path(cfg.lab_root))
    own_pms = pms is None
    if own_pms:
        pms = PmsProcessMac(cfg, iso)
    run_id = uuid.uuid4().hex
    t0 = time.monotonic()
    deadline = t0 + window_deadline_s
    meta = {"tool": "plex_bench_mac/1", "mode": "prepare-hold", "run_id": run_id,
            "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "window_deadline_s": window_deadline_s, "stop_reserve_s": stop_reserve_s,
            "auth": "token-file (content never logged)" if token else "loopback-unauth (no token)",
            "token_present": bool(token), "sessions": [], "waves": []}
    binary = Path(cfg.app) / "Contents" / "MacOS" / "Plex Media Server"
    config_paths = {"app": cfg.app, "binary": str(binary),
                    "lab_root": str(iso.lab), "support_dir": str(iso.lab_support),
                    "tmp_dir": str(iso.lab_tmp), "media_dir": str(iso.lab_media),
                    "default_support_path": str(iso.default_support),
                    "prefs_plist_path": str(iso.prefs_plist),
                    "prefs_domain": PREFS_DOMAIN,
                    "pms_log": str(Path(cfg.lab_root) / "pms.log"),
                    "out_dir": str(out_dir), "result_file": str(out_dir / "result.json"),
                    "ready_file": str(ready_path), "signal_file": str(signal_path),
                    "fixture": cfg.fixture or None,
                    "profile": asdict(cfg.profile)}  # fields only; token is not a field
    meta["config_paths"] = config_paths
    meta["token_file"] = token_file or None  # path only, never content
    ready = {"run_id": run_id, "state": "starting", "base_url": cfg.base_url,
             "signal_file": str(signal_path), "ready_file": str(ready_path),
             "window_deadline_s": window_deadline_s, "stop_reserve_s": stop_reserve_s,
             "deadline_epoch": time.time() + window_deadline_s,
             "auth": meta["auth"], "token_present": bool(token),
             "token_file": token_file or None,  # path only; root creates it 0600
             "config": config_paths,
             "log_paths": {"pms_log": config_paths["pms_log"],
                           "result_file": config_paths["result_file"]}}
    restore_auth = _install_auth_http(token, cfg.base_url)
    try:
        meta["pms_pid"] = pms.start(); root_pid = pms.pid
        ready.update({"pid": pms.pid, "pgid": getattr(pms, "pgid", None)})
        if getattr(pms, "log_path", None):
            ready["log_paths"]["pms_log"] = str(pms.log_path); ready["config"]["pms_log"] = str(pms.log_path)
            meta["config_paths"]["pms_log"] = str(pms.log_path)
        meta["identity"] = pms.wait_identity(); ready["identity"] = meta["identity"]
        ready["state"] = "waiting_for_signal"
        ready["hint"] = ("root: claim privately + create/scan library, then write signal JSON "
                          "{\"action\":\"continue\",\"rating_key\":\"<key>\",\"duration_s\":10} "
                          "or {\"action\":\"abort\"} to signal_file")
        publish_ready(ready_path, ready)
        not_before = ready_path.stat().st_mtime
        def _poll(remaining):
            ready["seconds_to_stop_reserve"] = round(remaining - stop_reserve_s, 1)
            try:
                s, _, b = pb.http_get(cfg.base_url + "/identity", timeout=3)
                if s == 200:
                    ready["identity"] = {"claimed": ET.fromstring(b).get("claimed"),
                                         "version": ET.fromstring(b).get("version")}
            except Exception:
                pass
            publish_ready(ready_path, ready)
        sig = wait_for_signal(signal_path, run_id, not_before, deadline, stop_reserve_s,
                              is_alive=lambda: pms.proc is None or pms.proc.poll() is None,
                              on_poll=_poll)
        meta["signal"] = {k: v for k, v in sig.items()}  # signal carries no token by contract
        if sig["action"] == "abort":
            ready["state"] = "aborted_by_signal"; meta["ok"] = False
            meta["error"] = "aborted by root signal before benchmark"
            return meta
        # Apply the chosen tiny benchmark, capped by the remaining deadline.
        if sig.get("rating_key"):
            cfg.rating_key = str(sig["rating_key"])
        cfg.duration_s = float(sig.get("duration_s", min(cfg.duration_s, 10.0)))
        cfg.concurrency = min(int(sig.get("concurrency", cfg.concurrency)), 2)
        cfg.repeats = 1
        remaining = deadline - time.monotonic()
        cfg.timeout_s = session_timeout_for(remaining, cfg.timeout_s, stop_reserve_s)
        ready["state"] = "running_benchmark"
        ready["benchmark"] = {"rating_key": cfg.rating_key, "duration_s": cfg.duration_s,
                              "timeout_s": cfg.timeout_s, "video_resolution": cfg.profile.video_resolution,
                              "video_codec": cfg.profile.video_codec}
        publish_ready(ready_path, ready)
        if token:
            cfg.profile._token = token  # TokenProfile instance supplied by caller
        bench = run_bench or (lambda: _run_waves(cfg, root_pid, out_dir))
        result = bench()
        meta["sessions"] = result["sessions"]; meta["waves"] = result["waves"]
        meta["verify"] = verify_all(cfg, out_dir, meta["sessions"])
        v = meta["verify"]
        if v.get("checked") and not (v.get("matches_codec") and v.get("matches_resolution")):
            meta["ok"] = False; meta["error"] = "verify mismatch: codec/resolution in at least one session"
        elif cfg.preserve_media and meta["sessions"] and not v.get("checked"):
            meta["ok"] = False; meta["error"] = "verify incomplete"
        else:
            meta["ok"] = True
        ready["state"] = "benchmark_done"
    except Exception as e:
        meta["ok"] = False; meta["error"] = pb.sanitize(str(e))
        ready["state"] = "failed"
    finally:
        # Stop always: SIGTERM->SIGKILL only our pgid, inside the deadline reserve.
        ready["state"] = "stopping" if ready.get("state") not in ("aborted_by_signal",) else ready["state"]
        try:
            meta["pms_stop"] = pms.stop()
        except Exception as e:
            meta["pms_stop"] = {"error": pb.sanitize(str(e))}
        if own_pms:
            try:
                meta["isolation_restore"] = iso.restore()
            except Exception as e:
                meta["isolation_restore"] = {"error": pb.sanitize(str(e))}
        meta["elapsed_s"] = round(time.monotonic() - t0, 3)
        meta["within_deadline"] = meta["elapsed_s"] <= window_deadline_s
        meta["port_32400_connectable_after"] = port_connectable(cfg.base_url) if own_pms else None
        ready["final_state"] = "done" if meta.get("ok") else "stopped_with_error"
        ready["elapsed_s"] = meta["elapsed_s"]; ready["within_deadline"] = meta["within_deadline"]
        try:
            publish_ready(ready_path, ready)
        except Exception:
            pass
        restore_auth()
    return meta

def _run_waves(cfg: MacConfig, root_pid, out_dir: Path) -> dict:
    sessions, waves = [], []
    for wave in range(cfg.repeats):
        t_wave = time.monotonic(); wave_sessions = []
        with concurrent.futures.ThreadPoolExecutor(max_workers=cfg.concurrency) as ex:
            futs = [ex.submit(run_session_mac, cfg, root_pid, out_dir, wave * cfg.concurrency + i)
                    for i in range(cfg.concurrency)]
            for f in futs:
                wave_sessions.append(f.result())
        wall = round(time.monotonic() - t_wave, 3)
        media_total = round(sum(s.get("actual_media_s") or 0 for s in wave_sessions), 3)
        waves.append({"wave": wave, "wall_s": wall, "concurrency": cfg.concurrency,
                      "sessions": len(wave_sessions), "actual_media_total_s": media_total,
                      "aggregate_media_per_wall_s": round(media_total / wall, 3) if wall > 0 else None})
        sessions.extend(wave_sessions)
    return {"sessions": sessions, "waves": waves}

def fixture_info(path: str) -> dict:
    info = {"path": path, "exists": False, "bytes": None, "sha256": None, "duration_s": None}
    p = Path(path)
    if not p.exists():
        return info
    info.update({"exists": True, "bytes": p.stat().st_size, "sha256": pb.sha256_file(p)})
    try:
        r = subprocess.run(["ffprobe", "-v", "error", "-show_entries", "format=duration",
                            "-of", "csv=p=0", str(p)], capture_output=True, text=True, timeout=30)
        if r.returncode == 0:
            info["duration_s"] = round(float(r.stdout.strip()), 3)
    except Exception:
        pass
    return info

def preflight(cfg: MacConfig) -> dict:
    iso = MacIsolation(Path(cfg.lab_root))
    binary = Path(cfg.app) / "Contents" / "MacOS" / "Plex Media Server"
    transcoder = Path(cfg.app) / "Contents" / "MacOS" / "Plex Transcoder"
    strings_note = None
    try:
        r = subprocess.run(["/usr/bin/strings", str(binary)], capture_output=True, text=True, timeout=60)
        strings_note = {"env_var_in_binary": "PLEX_MEDIA_SERVER_APPLICATION_SUPPORT_DIR" in r.stdout,
                        "videotoolbox_in_binary": "videotoolbox" in r.stdout.lower(),
                        "bundle_id_in_binary": PREFS_DOMAIN in r.stdout}
    except Exception as e:
        strings_note = {"error": str(e)}
    return {"platform": sys.platform, "app": cfg.app, "binary_exists": binary.exists(),
            "transcoder_exists": transcoder.exists(), "binary_strings": strings_note,
            "isolation_preexisting": iso.check_preexisting(),
            "port_32400_connectable": port_connectable(cfg.base_url),
            "fixture": fixture_info(cfg.fixture) if cfg.fixture else None,
            "home": str(Path.home()), "window_granted": cfg.window_granted,
            "plan": START_PLAN}

def main(argv=None):
    ap = argparse.ArgumentParser(description="Native macOS authentic Plex transcode driver")
    ap.add_argument("--app", default=DEFAULT_APP); ap.add_argument("--lab-root", default=DEFAULT_LAB)
    ap.add_argument("--fixture", default=""); ap.add_argument("--base-url", default="http://127.0.0.1:32400")
    ap.add_argument("--rating-key", default="1"); ap.add_argument("--duration", type=float, default=32.0)
    ap.add_argument("--mode", choices=["fast", "paced"], default="fast")
    ap.add_argument("--concurrency", type=int, default=1); ap.add_argument("--repeats", type=int, default=1)
    ap.add_argument("--timeout", type=float, default=120.0); ap.add_argument("--startup-timeout", type=float, default=60.0)
    ap.add_argument("--preserve-media", action="store_true"); ap.add_argument("--out", default="")
    ap.add_argument("--video-codec", default="h264"); ap.add_argument("--video-resolution", default="640x360")
    ap.add_argument("--max-video-bitrate", type=int, default=2000); ap.add_argument("--audio-codec", default="aac")
    ap.add_argument("--window-granted", action="store_true",
                    help="REQUIRED to launch PMS: root grant + Ether stopped + 32400 free")
    ap.add_argument("--no-launch", action="store_true", help="use an operator-started server (identity only unless --pms-pid)")
    ap.add_argument("--pms-pid", type=int, default=None)
    ap.add_argument("--preflight-only", action="store_true"); ap.add_argument("--plan-only", action="store_true")
    ap.add_argument("--prepare-hold", action="store_true",
                    help="launch+identity, publish readiness, hold for root continue signal, then tiny benchmark; always stops within --window-deadline-s")
    ap.add_argument("--ready-file", default="", help="default: <lab-root>/ready.json")
    ap.add_argument("--signal-file", default="", help="default: <lab-root>/continue.json (root writes it after claim/library)")
    ap.add_argument("--window-deadline-s", type=float, default=300.0, help="hard 5-minute window incl. stop of all children")
    ap.add_argument("--stop-reserve-s", type=float, default=30.0, help="tail of deadline reserved for SIGTERM/SIGKILL + restore")
    ap.add_argument("--token-file", default="", help="private file with Plex token (first line); content never logged. Omit for loopback-unauth")
    a = ap.parse_args(argv)
    cfg = MacConfig(app=a.app, lab_root=a.lab_root, fixture=a.fixture, base_url=a.base_url,
                    rating_key=a.rating_key, duration_s=a.duration, mode=a.mode, concurrency=a.concurrency,
                    repeats=a.repeats, timeout_s=a.timeout, startup_timeout_s=a.startup_timeout,
                    preserve_media=a.preserve_media, window_granted=a.window_granted, pms_pid=a.pms_pid,
                    profile=TokenProfile(video_codec=a.video_codec, video_resolution=a.video_resolution,
                                       max_video_bitrate_kbps=a.max_video_bitrate, audio_codec=a.audio_codec))
    if a.plan_only:
        print(json.dumps({"plan": START_PLAN, "preflight": preflight(cfg)}, indent=2)); return 0
    if a.preflight_only:
        print(json.dumps(preflight(cfg), indent=2)); return 0
    if a.duration <= 0 or a.concurrency < 1 or a.repeats < 1:
        ap.error("duration/concurrency/repeats must be positive")
    out_dir = Path(a.out) if a.out else Path(f"/tmp/plexbench-mac-{int(time.time())}")
    out_dir.mkdir(parents=True, exist_ok=True)
    if a.prepare_hold:
        if a.no_launch:
            ap.error("--prepare-hold owns the PMS launch; do not combine with --no-launch")
        token = load_token_file(a.token_file) if a.token_file else ""
        if token:
            cfg.profile._token = token
        ready_path = Path(a.ready_file) if a.ready_file else Path(cfg.lab_root) / "ready.json"
        signal_path = Path(a.signal_file) if a.signal_file else Path(cfg.lab_root) / "continue.json"
        meta = run_prepare_hold(cfg, out_dir, ready_path, signal_path,
                                window_deadline_s=a.window_deadline_s, stop_reserve_s=a.stop_reserve_s,
                                token=token, token_file=a.token_file)
        meta["config"] = {**asdict(cfg), "profile": asdict(cfg.profile)}  # profile fields only; token is not a field
        meta["fixture"] = fixture_info(cfg.fixture) if cfg.fixture else None
        (out_dir / "result.json").write_text(json.dumps(meta, indent=2))
        print(json.dumps({"ok": meta.get("ok", False), "mode": "prepare-hold", "out": str(out_dir),
                          "ready_file": str(ready_path), "within_deadline": meta.get("within_deadline"),
                          **({"error": meta["error"]} if meta.get("error") else {})}))
        return 0 if meta.get("ok") else 1
    iso = MacIsolation(Path(cfg.lab_root))
    meta = {"tool": "plex_bench_mac/1", "driver": "plex_bench authentic HTTP (Android profile, explicit target)",
            "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "config": {**asdict(cfg), "profile": asdict(cfg.profile)},
            "fixture": fixture_info(cfg.fixture) if cfg.fixture else None,
            "preflight": preflight(cfg), "plan": START_PLAN}
    pms = None; root_pid = a.pms_pid; sessions = []; waves = []
    _token = load_token_file(a.token_file) if a.token_file else ""
    if _token:
        cfg.profile._token = _token
    meta["auth"] = "token-file (content never logged)" if _token else "loopback-unauth (no token)"
    meta["token_file"] = a.token_file or None
    restore_auth = _install_auth_http(_token, cfg.base_url)
    try:
        if not a.no_launch:
            pms = PmsProcessMac(cfg, iso); meta["pms_pid"] = pms.start(); root_pid = pms.pid
            meta["isolation"] = iso.check_preexisting()
            meta["identity"] = pms.wait_identity()
        else:
            s, _, b = pb.http_get(cfg.base_url + "/identity", timeout=10)
            root = ET.fromstring(b)
            meta["identity"] = {"claimed": root.get("claimed"), "version": root.get("version")}
        meta["pms_version"] = meta.get("identity", {}).get("version")
        # Remaining-window note: claim/library setup is root/operator work, not this driver.
        for wave in range(cfg.repeats):
            t_wave = time.monotonic(); wave_sessions = []
            with concurrent.futures.ThreadPoolExecutor(max_workers=cfg.concurrency) as ex:
                futs = [ex.submit(run_session_mac, cfg, root_pid, out_dir, wave * cfg.concurrency + i)
                        for i in range(cfg.concurrency)]
                for f in futs:
                    wave_sessions.append(f.result())
            wall = round(time.monotonic() - t_wave, 3)
            media_total = round(sum(s.get("actual_media_s") or 0 for s in wave_sessions), 3)
            waves.append({"wave": wave, "wall_s": wall, "concurrency": cfg.concurrency,
                          "sessions": len(wave_sessions), "actual_media_total_s": media_total,
                          "aggregate_media_per_wall_s": round(media_total / wall, 3) if wall > 0 else None})
            sessions.extend(wave_sessions)
        meta["sessions"] = sessions; meta["waves"] = waves
        meta["verify"] = verify_all(cfg, out_dir, sessions)
        v = meta["verify"]
        if v.get("checked") and not (v.get("matches_codec") and v.get("matches_resolution")):
            meta["ok"] = False; meta["error"] = "verify mismatch: codec/resolution in at least one session"
        elif cfg.preserve_media and sessions and not v.get("checked"):
            meta["ok"] = False; meta["error"] = f"verify incomplete: {v.get('reason', 'not all sessions checked')}"
        else:
            meta["ok"] = True
    except Exception as e:
        meta["ok"] = False; meta["error"] = pb.sanitize(str(e)); meta["sessions"] = sessions; meta["waves"] = waves
    finally:
        if pms:
            meta["pms_stop"] = pms.stop(); meta["isolation_restore"] = iso.restore()
            meta["port_32400_connectable_after"] = port_connectable(cfg.base_url)
        restore_auth()
    (out_dir / "result.json").write_text(json.dumps(meta, indent=2))
    print(json.dumps({"ok": meta["ok"], "out": str(out_dir), "sessions": len(sessions),
                      **({"error": meta["error"]} if not meta["ok"] else {}),
                      "actual_media_s": [s.get("actual_media_s") for s in sessions], "waves": waves}))
    return 0 if meta["ok"] else 1

if __name__ == "__main__":
    sys.exit(main())
