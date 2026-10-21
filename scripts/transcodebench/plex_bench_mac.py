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
DEFAULT_APP = "/Users/braedonsaunders/.bb/thread-storage/thr_dbguekh66b/transcode-lab/plex-macos-relocated/Plex Media Server.app"
DEFAULT_LAB = "/Users/braedonsaunders/.bb/thread-storage/thr_dbguekh66b/transcode-lab/plex-macos-lab"
PREFS_DOMAIN = "com.plexapp.plexmediaserver"
# LetsMove-style self-relocation guard key found (twice) in the PMS binary
# strings. Upstream LetsMove returns early when this is true, but Plex's
# vendored copy is NOT proven to behave identically: this is an unproven
# mitigation only. The load-bearing protection is the relocation audit in
# PmsProcessMac (new Applications copy + escaped process provenance), which
# fails cleanup truthfully if relocation happens anyway.
RELOC_SUPPRESS_KEY = "moveToApplicationsFolderAlertSuppress"
HW_TOKENS = ("videotoolbox", "video_toolbox", "vaapi", "nvenc", "qsv")

# ---------------------------------------------------------------- plan ----
START_PLAN = {
    "preconditions": [
        "root (thr_dbguekh66b) explicitly grants the window in this thread",
        "runtime owner (thr_gfsnk4z9fi) confirms Ether stopped; nothing listening on 32400",
        "driver preflight passes: default Plex data path and prefs plist absent (or our own symlink), fixture present, port 32400 not connectable",
    ],
    "window_5min_timeline": [
        "T+0:00  python3 plex_bench_mac.py --preflight-only  (read-only: data/prefs absent, ~/Applications+//Applications Plex absent, no PMS process, port 32400 free)",
        "T+0:30  launch ONE command (native launch plan below): --prepare-hold --window-granted; prepare() first writes lab-owned CFPreferences moveToApplicationsFolderAlertSuppress=true via /usr/bin/defaults (domain was absent; UNPROVEN vs Plex's vendored relocation), then PMS starts in its own pgid from the relocated lab app; driver waits /identity only from the still-owned leader, audits for escaped/relocated PMS, then publishes <lab>/ready.json and HOLDS",
        "T+1:30  root privately claims server with Plex Pass account (credentials never enter driver except optional --token-file, whose content is never logged)",
        "T+2:30  root creates/scans 'Lab' library, then writes <lab>/continue.json {\"action\":\"continue\",\"rating_key\":\"<key>\",\"duration_s\":10} (or {\"action\":\"abort\"}); stale/wrong-run_id signals are rejected",
        "T+3:30  driver runs tiny smoke for that ratingKey only, timeout capped at deadline minus 30s stop reserve: source.mkv 60s 640x360, 10s, Android h264 640x360@2000k; require videotoolbox in captured argv (else software fallback)",
        "T+4:30  driver stops session, SIGTERM owned pgid even if leader exited (SIGKILL after timeout), reaps provably-escaped relaunches by exact lab/relocated-app provenance, audits ps; ONLY if clean does it restore original absence of data path/prefs, then verifies port 32400 free",
        "T+5:00  window ends; result.json + PMS log path reported to root; Ether may be restarted by its owner only after this report",
    ],
    "native_launch_plan_for_root_validation": [
        "precondition (outside driver): root grants window, Ether stopped, nothing on 32400, ~/Library Plex data+prefs absent, no Plex app in ~/Applications or /Applications, no PMS process running",
        "exact app: /Users/braedonsaunders/.bb/thread-storage/thr_dbguekh66b/transcode-lab/plex-macos-relocated/Plex Media Server.app (DEFAULT_APP)",
        "command: cd scripts/transcodebench && python3 plex_bench_mac.py --prepare-hold --window-granted --window-deadline-s 300 --stop-reserve-s 30 --app '<above app>' [--token-file <root-owned 0600 file>] --out <lab out dir>",
        "proof root must collect in that window (suppression is not assumed): after identity, `ls ~/Applications '/Applications'` shows no new Plex Media Server.app, `ps -axo pid,ppid,pgid,args` shows PMS only under the lab app path in the driver pgid, and ready.json identity came from the owned leader",
        "if a relocated copy or escaped process appears: driver fails the run, reaps only provably-owned pids/pgids, leaves symlink+prefs in place, and reports cleanup NOT clean; root manually removes the relocated copy and only then restores Ether",
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
    def __init__(self, lab_root: Path, home: Path | None = None,
                 relocation_destinations: list[Path] | None = None,
                 defaults_runner=None):
        self.lab = Path(lab_root)
        self.home = Path(home) if home else Path.home()
        self.default_support = self.home / "Library" / "Application Support" / "Plex Media Server"
        self.prefs_plist = self.home / "Library" / "Preferences" / f"{PREFS_DOMAIN}.plist"
        self.lab_support = self.lab / "support"
        self.lab_tmp = self.lab / "tmp"
        self.lab_media = self.lab / "media"
        self.created_symlink = False
        self.prefs_existed_before = False
        self.prefs_created_by_us = False
        # Where a self-relocating PMS would copy itself. An escaped copy is
        # only provably ours if the destination was absent before launch.
        self.relocation_destinations = [Path(p) for p in relocation_destinations] if relocation_destinations is not None else [
            self.home / "Applications" / "Plex Media Server.app",
            Path("/Applications/Plex Media Server.app")]
        # Injectable (list[str]) -> CompletedProcess-like; tests never touch
        # the real CFPreferences domain.
        self._defaults = defaults_runner or (
            lambda argv: subprocess.run(argv, capture_output=True, text=True, timeout=15))

    def check_preexisting(self) -> dict:
        support_state = ("symlink->ours" if self.default_support.is_symlink()
                         and self.default_support.resolve() == self.lab_support.resolve()
                         else "present" if os.path.lexists(self.default_support) else "absent")
        prefs_state = "present" if os.path.lexists(self.prefs_plist) else "absent"
        return {"default_support": support_state, "prefs_plist": prefs_state,
                "relocation_destinations": {str(p): ("present" if os.path.lexists(p) else "absent")
                                            for p in self.relocation_destinations},
                "home": str(self.home)}

    def _write_reloc_suppress(self) -> None:
        """Lab-owned CFPreferences: exactly one key, domain absent before.

        Written via /usr/bin/defaults so cfprefsd serves it to the PMS
        process launched afterwards. Unproven against Plex's vendored
        relocation code -- see RELOC_SUPPRESS_KEY note.
        """
        r = self._defaults(["/usr/bin/defaults", "write", PREFS_DOMAIN,
                            RELOC_SUPPRESS_KEY, "-bool", "true"])
        if getattr(r, "returncode", 1) != 0:
            raise BenchError(f"defaults write {RELOC_SUPPRESS_KEY} failed rc={getattr(r, 'returncode', '?')}: "
                             f"{pb.sanitize(str(getattr(r, 'stderr', '') or ''))[:200]}")
        self.prefs_created_by_us = True

    def prepare(self) -> dict:
        state = self.check_preexisting()
        if state["default_support"] == "present":
            raise BenchError(f"pre-existing Plex data at {self.default_support}; refusing to touch it")
        if state["prefs_plist"] == "present":
            raise BenchError(f"pre-existing prefs {self.prefs_plist}; refusing to touch it")
        for dest, st in state["relocation_destinations"].items():
            if st == "present":
                raise BenchError(f"pre-existing Plex app at relocation destination {dest}; "
                                 "an escaped copy there could not be proven ours, refusing to launch")
        self.prefs_existed_before = False
        for d in (self.lab_support, self.lab_tmp, self.lab_media):
            d.mkdir(parents=True, exist_ok=True)
        if state["default_support"] == "absent":
            self.default_support.parent.mkdir(parents=True, exist_ok=True)
            os.symlink(self.lab_support, self.default_support)
            self.created_symlink = True
        try:
            self._write_reloc_suppress()
        except Exception:
            # Do not leave a half-prepared isolation behind on failure.
            if self.created_symlink and self.default_support.is_symlink():
                self.default_support.unlink(); self.created_symlink = False
            raise
        return {"strategy": "env PLEX_MEDIA_SERVER_APPLICATION_SUPPORT_DIR (primary, Mac honoring unverified) "
                            "+ default-path symlink guard (load-bearing) + lab-owned CFPreferences "
                            f"({RELOC_SUPPRESS_KEY}=true, relocation suppression UNPROVEN) + restore-absence",
                "env_support_dir": str(self.lab_support), "symlink_created": self.created_symlink,
                "reloc_suppress_attempted": True, "reloc_suppress_proven": False,
                "prefs_domain": PREFS_DOMAIN, "home_repurposed": False, **state}

    def restore(self) -> dict:
        """Restore original absence. Raises BenchError if that cannot be
        verified -- callers must never report cleanup on a silent failure."""
        actions, errors = [], []
        if self.created_symlink:
            if self.default_support.is_symlink() \
                    and self.default_support.resolve() == self.lab_support.resolve():
                self.default_support.unlink(); actions.append("removed lab symlink (default path absent again)")
            elif os.path.lexists(self.default_support):
                errors.append(f"default support path is no longer our symlink ({self.default_support}); left untouched")
        # CFPreferences: remove only the domain this run created (absent before).
        if self.prefs_created_by_us and not self.prefs_existed_before:
            r = self._defaults(["/usr/bin/defaults", "delete", PREFS_DOMAIN])
            if os.path.lexists(self.prefs_plist):
                try:
                    Path(self.prefs_plist).unlink()
                except OSError as e:
                    errors.append(f"could not remove created prefs plist: {e}")
            elif getattr(r, "returncode", 0) not in (0,):
                actions.append("defaults delete reported no domain; plist already absent")
            if not os.path.lexists(self.prefs_plist):
                actions.append("removed CFPreferences domain/plist created by this run")
        after = self.check_preexisting()
        if after["default_support"] != "absent":
            errors.append(f"default support not absent after restore: {after['default_support']}")
        if self.prefs_created_by_us and after["prefs_plist"] != "absent":
            errors.append(f"prefs plist not absent after restore: {after['prefs_plist']}")
        if errors:
            raise BenchError("isolation restore incomplete: " + "; ".join(errors))
        return {"actions": actions, "after": after, "restored_absent": True}

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

# ---------------------------------------------------------- PMS audit ---
def _exe_under(args: str, app: Path) -> bool:
    """True if the ps args' executable lives inside `app` (paths contain
    spaces, so prefix-match the known binary path, never whitespace-split)."""
    exe = str(app / "Contents" / "MacOS" / "Plex Media Server")
    return args == exe or args.startswith(exe + " ") or args.startswith(exe + "\t")

def _is_pms_like(args: str) -> bool:
    return "Plex Media Server" in args or "Plex Transcoder" in args

def classify_processes(rows, lab_app, relocation_apps, absent_before: set,
                       baseline_pids: set, owned_pgid) -> dict:
    """Split PMS-like rows into provably-ours vs uncertain. Nothing in the
    uncertain bucket is ever signalled.

    Provably ours: in the owned pgid; or executable under the lab app and
    not a pre-launch baseline pid; or executable under a relocation app
    that was absent before launch (only our run could have created it).
    """
    owned, escaped, uncertain = [], [], []
    for r in rows:
        if not _is_pms_like(r["args"]) or r["pid"] in baseline_pids:
            continue
        if owned_pgid is not None and r["pgid"] == owned_pgid:
            owned.append(r)
        elif _exe_under(r["args"], Path(lab_app)):
            escaped.append(r)  # relaunched from the lab app, new session/pgid
        elif any(str(d) in absent_before and _exe_under(r["args"], Path(d)) for d in relocation_apps):
            escaped.append(r)  # escaped self-relocated copy
        else:
            uncertain.append(r)
    return {"owned": owned, "escaped": escaped, "uncertain": uncertain}

def stop_is_clean(stop: dict | None) -> bool:
    """A stop result only counts as clean when it proves it."""
    if not isinstance(stop, dict) or stop.get("error"):
        return False
    if "clean" in stop:
        return bool(stop["clean"]) and not stop.get("remaining_pids") \
            and not stop.get("escaped_remaining") and not stop.get("uncertain_pids")
    return not stop.get("remaining_pids") and not stop.get("audit_error")

class PmsProcessMac:
    """Owns exactly one PMS process group, plus provably-escaped copies.

    stop() signals the owned pgid even when the leader already exited 0
    (the self-relocation failure mode: parent exits, children/relaunch
    survive). It audits with ps before declaring clean: any audit or
    permission error, any surviving owned/escaped pid, and any PMS-like
    process whose provenance is uncertain makes clean=False. Uncertain
    processes are never signalled.
    """
    def __init__(self, cfg: MacConfig, isolation: MacIsolation):
        self.cfg = cfg; self.iso = isolation
        self.proc = None; self.pid = None; self.pgid = None; self.log_path = None
        self.relocation_before: dict[str, bool] = {}
        self.baseline_pms_pids: set = set()
        self.term_timeout_s = 10.0; self.kill_timeout_s = 5.0; self.poll_interval_s = 0.1
        # Injectable seams for offline tests (no real ps/signals there).
        self._snapshot = ps_snapshot; self._killpg = os.killpg; self._kill = os.kill

    def _classify(self, rows) -> dict:
        absent = {p for p, existed in self.relocation_before.items() if not existed}
        return classify_processes(rows, self.cfg.app, self.iso.relocation_destinations,
                                  absent, self.baseline_pms_pids, self.pgid)

    def audit_escape(self, rows=None) -> dict:
        """Current relocation/escape state. Raises BenchError on ps failure
        so callers cannot mistake an audit failure for 'all clear'."""
        rows = rows if rows is not None else self._snapshot()
        cls = self._classify(rows)
        new_dests = [str(d) for d in self.iso.relocation_destinations
                     if not self.relocation_before.get(str(d), True) and os.path.lexists(d)]
        return {"escaped_pids": [r["pid"] for r in cls["escaped"]],
                "uncertain_pids": [r["pid"] for r in cls["uncertain"]],
                "relocated_destinations": new_dests}

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
        # Provenance baseline BEFORE launch (audit failure = no launch).
        try:
            rows = self._snapshot()
        except Exception as e:
            raise BenchError(f"pre-launch process audit failed, refusing to launch: {e}")
        foreign = [r for r in rows if _is_pms_like(r["args"])]
        if foreign:
            raise BenchError(f"PMS-like process already running before launch (pids "
                             f"{[r['pid'] for r in foreign]}); cannot prove provenance, refusing to launch")
        self.baseline_pms_pids = {r["pid"] for r in foreign}
        pre = self.iso.check_preexisting()
        self.relocation_before = {p: (s == "present") for p, s in pre["relocation_destinations"].items()}
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
                    # An escaped relaunch can answer /identity too: identity
                    # only counts from the still-owned leader, with no escape.
                    if self.proc and self.proc.poll() is not None:
                        raise BenchError(f"PMS leader exited rc={self.proc.returncode} but /identity answered "
                                         f"(possible escaped relaunch); log={self.log_path}")
                    try:
                        esc = self.audit_escape()
                    except Exception as e:
                        raise BenchError(f"post-identity process audit failed: {e}")
                    if esc["escaped_pids"] or esc["relocated_destinations"] or esc["uncertain_pids"]:
                        raise BenchError(f"PMS self-relocation/escape detected at identity: {esc}; log={self.log_path}")
                    root = ET.fromstring(b)
                    return {"claimed": root.get("claimed"), "version": root.get("version"),
                            "machineIdentifier_present": bool(root.get("machineIdentifier"))}
            except Exception as e:
                last = str(e)
            time.sleep(0.5)
        raise BenchError(f"PMS identity not ready in {self.cfg.startup_timeout_s}s: {last}")

    def _signal_group(self, pgid, sig, result, errors):
        if pgid is None:
            return
        try:
            self._killpg(pgid, sig); result["signalled"] = True
            result.setdefault("signalled_pgids", []).append(pgid)
        except ProcessLookupError:
            pass  # group already gone: the desired end state
        except PermissionError as e:
            errors.append(f"permission denied signalling pgid {pgid} with {sig}: {e}")
        except Exception as e:
            errors.append(f"signalling pgid {pgid} with {sig} failed: {e}")

    def _wait_until_gone(self, timeout_s):
        """Poll until no owned/escaped rows remain. Returns (cls, rows) or
        raises BenchError when the audit itself fails."""
        deadline = time.monotonic() + timeout_s
        while True:
            rows = self._snapshot()
            cls = self._classify(rows)
            if not cls["owned"] and not cls["escaped"]:
                return cls, rows
            if time.monotonic() >= deadline:
                return cls, rows
            time.sleep(self.poll_interval_s)

    def stop(self) -> dict:
        result = {"pgid": self.pgid, "signalled": False, "remaining_pids": [],
                  "escaped_pids": [], "escaped_remaining": [], "uncertain_pids": [],
                  "relocated_destinations": [], "audit_error": None,
                  "errors": [], "clean": False}
        if self.pid is None and self.pgid is None:
            result["clean"] = True  # never started: nothing owned to reap
            return result
        errors = result["errors"]
        # 1) TERM the owned group even if the leader already exited 0.
        self._signal_group(self.pgid, signal.SIGTERM, result, errors)
        if self.proc is not None:
            try:
                self.proc.wait(timeout=self.term_timeout_s)
            except Exception:
                pass
        # 2) Audit; escalate to KILL for the owned group AND any provably
        #    escaped relaunch (new pgid under the lab/relocated app).
        try:
            cls0 = self._classify(self._snapshot())
            for pgid in {r["pgid"] for r in cls0["escaped"] if r["pgid"] != self.pgid}:
                self._signal_group(pgid, signal.SIGTERM, result, errors)
            cls, _ = self._wait_until_gone(self.term_timeout_s)
            targets = cls["owned"] + cls["escaped"]
            if targets:
                pgids = {r["pgid"] for r in targets}
                for pgid in pgids:
                    self._signal_group(pgid, signal.SIGKILL, result, errors)
                cls, _ = self._wait_until_gone(self.kill_timeout_s)
                targets = cls["owned"] + cls["escaped"]
                # Last resort per-pid KILL for escapees whose group signal
                # could not cover them (still only provably-ours pids).
                for r in targets:
                    try:
                        self._kill(r["pid"], signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    except PermissionError as e:
                        errors.append(f"permission denied killing escaped pid {r['pid']}: {e}")
                cls, _ = self._wait_until_gone(self.kill_timeout_s)
            result["remaining_pids"] = sorted({r["pid"] for r in cls["owned"]})
            result["escaped_pids"] = sorted({r["pid"] for r in cls["escaped"]})
            result["escaped_remaining"] = result["escaped_pids"]
            result["uncertain_pids"] = sorted({r["pid"] for r in cls["uncertain"]})
            result["relocated_destinations"] = [str(d) for d in self.iso.relocation_destinations
                                                if not self.relocation_before.get(str(d), True)
                                                and os.path.lexists(d)]
        except Exception as e:
            # Audit failure is NOT 'no remaining processes'.
            result["audit_error"] = pb.sanitize(str(e))[:300]
            result["remaining_pids"] = []
        result["clean"] = (not errors and result["audit_error"] is None
                           and not result["remaining_pids"] and not result["escaped_remaining"]
                           and not result["uncertain_pids"]
                           and not result["relocated_destinations"])
        if errors:
            result["error"] = "; ".join(errors)
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

def finalize_cleanup(pms, iso, own_pms: bool) -> dict:
    """Stop, then restore isolation ONLY on a proven-clean stop.

    On any uncertainty (survivors, escaped/relocated copies, audit or
    permission errors, restore not verifying absence) the symlink and
    lab-owned prefs are left in place for root's manual recovery and
    clean=False -- cleanup is never declared successful when uncertain.
    """
    out = {"pms_stop": None, "isolation_restore": None, "clean": False, "errors": []}
    try:
        out["pms_stop"] = pms.stop()
    except Exception as e:
        out["pms_stop"] = {"error": pb.sanitize(str(e))}
    if not stop_is_clean(out["pms_stop"]):
        out["errors"].append(f"owned PMS not proven gone (stop={out['pms_stop']}); "
                             "isolation left in place for root manual recovery")
        return out
    if own_pms:
        try:
            out["isolation_restore"] = iso.restore()
        except Exception as e:
            out["isolation_restore"] = {"error": pb.sanitize(str(e))}
            out["errors"].append(f"isolation restore failed: {e}")
            return out
    out["clean"] = True
    return out

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
        # Stop always, inside the deadline reserve. Isolation is restored
        # only on a proven-clean stop; otherwise cleanup fails truthfully.
        ready["state"] = "stopping" if ready.get("state") not in ("aborted_by_signal",) else ready["state"]
        cleanup = finalize_cleanup(pms, iso, own_pms)
        meta["pms_stop"] = cleanup["pms_stop"]; meta["isolation_restore"] = cleanup["isolation_restore"]
        meta["cleanup_clean"] = cleanup["clean"]
        if not cleanup["clean"]:
            meta["ok"] = False
            cerr = "cleanup NOT clean: " + "; ".join(cleanup["errors"])
            meta["error"] = f"{meta['error']}; {cerr}" if meta.get("error") else cerr
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
                        "reloc_suppress_key_in_binary": RELOC_SUPPRESS_KEY in r.stdout,
                        "bundle_id_in_binary": PREFS_DOMAIN in r.stdout}
    except Exception as e:
        strings_note = {"error": str(e)}
    try:
        prows = ps_snapshot()
        process_audit = {"pms_like_pids": [r["pid"] for r in prows if _is_pms_like(r["args"])],
                         "error": None}
    except Exception as e:
        process_audit = {"pms_like_pids": None, "error": str(e)}  # None = audit failed, not 'none running'
    return {"platform": sys.platform, "app": cfg.app, "binary_exists": binary.exists(),
            "transcoder_exists": transcoder.exists(), "binary_strings": strings_note,
            "process_audit": process_audit,
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
            cleanup = finalize_cleanup(pms, iso, True)
            meta["pms_stop"] = cleanup["pms_stop"]; meta["isolation_restore"] = cleanup["isolation_restore"]
            meta["cleanup_clean"] = cleanup["clean"]
            if not cleanup["clean"]:
                meta["ok"] = False
                cerr = "cleanup NOT clean: " + "; ".join(cleanup["errors"])
                meta["error"] = f"{meta['error']}; {cerr}" if meta.get("error") else cerr
            meta["port_32400_connectable_after"] = port_connectable(cfg.base_url)
        restore_auth()
    (out_dir / "result.json").write_text(json.dumps(meta, indent=2))
    print(json.dumps({"ok": meta["ok"], "out": str(out_dir), "sessions": len(sessions),
                      **({"error": meta["error"]} if not meta["ok"] else {}),
                      "actual_media_s": [s.get("actual_media_s") for s in sessions], "waves": waves}))
    return 0 if meta["ok"] else 1

if __name__ == "__main__":
    sys.exit(main())
