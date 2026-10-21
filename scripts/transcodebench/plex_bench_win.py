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
# Pointer-safe Win32 signatures: every handle-returning API must use a
# HANDLE (void*) restype, never the ctypes default c_int (truncates on x64).
WIN32_SIGNATURES = {
    "CreateToolhelp32Snapshot": ("HANDLE", ["DWORD", "DWORD"]),
    "OpenProcess": ("HANDLE", ["DWORD", "BOOL", "DWORD"]),
    "OpenThread": ("HANDLE", ["DWORD", "BOOL", "DWORD"]),
    "CreateJobObjectW": ("HANDLE", ["LPVOID", "LPCWSTR"]),
    "CreateProcessW": ("BOOL", ["LPCWSTR", "LPWSTR", "LPVOID", "LPVOID", "BOOL",
                                "DWORD", "LPVOID", "LPCWSTR", "LPVOID", "LPVOID"]),
    "CloseHandle": ("BOOL", ["HANDLE"]),
    "Process32FirstW": ("BOOL", ["HANDLE", "LPVOID"]),
    "Process32NextW": ("BOOL", ["HANDLE", "LPVOID"]),
    "GetProcessTimes": ("BOOL", ["HANDLE", "LPVOID", "LPVOID", "LPVOID", "LPVOID"]),
    "GetProcessMemoryInfo": ("BOOL", ["HANDLE", "LPVOID", "DWORD"]),
    "SetInformationJobObject": ("BOOL", ["HANDLE", "INT", "LPVOID", "DWORD"]),
    "QueryInformationJobObject": ("BOOL", ["HANDLE", "INT", "LPVOID", "DWORD", "LPDWORD"]),
    "AssignProcessToJobObject": ("BOOL", ["HANDLE", "HANDLE"]),
    "ResumeThread": ("DWORD", ["HANDLE"]),
    "WaitForSingleObject": ("DWORD", ["HANDLE", "DWORD"]),
    "GetExitCodeProcess": ("BOOL", ["HANDLE", "LPDWORD"]),
    "TerminateProcess": ("BOOL", ["HANDLE", "UINT"]),
    "ReadProcessMemory": ("BOOL", ["HANDLE", "LPVOID", "LPVOID", "SIZE_T", "SIZE_T_PTR"]),
    "NtQueryInformationProcess": ("NTSTATUS", ["HANDLE", "INT", "LPVOID", "ULONG", "PULONG"]),
}
_TYPE_MAP = None


def _win32_type_map():
    global _TYPE_MAP
    if _TYPE_MAP is None:
        _TYPE_MAP = {
            "HANDLE": ctypes.c_void_p, "DWORD": ctypes.c_ulong, "BOOL": ctypes.c_int,
            "INT": ctypes.c_int, "UINT": ctypes.c_uint, "LPVOID": ctypes.c_void_p,
            "LPCWSTR": ctypes.c_wchar_p, "LPWSTR": ctypes.c_wchar_p,
            "LPDWORD": ctypes.POINTER(ctypes.c_ulong),
            "SIZE_T": ctypes.c_size_t, "SIZE_T_PTR": ctypes.POINTER(ctypes.c_size_t),
            "NTSTATUS": ctypes.c_long, "ULONG": ctypes.c_ulong,
            "PULONG": ctypes.POINTER(ctypes.c_ulong),
        }
    return _TYPE_MAP


def configure_win32(kernel32=None, psapi=None, ntdll=None):
    """Apply WIN32_SIGNATURES to real DLLs (win32 only; no-op target in tests)."""
    if kernel32 is None:
        kernel32 = ctypes.windll.kernel32
    if psapi is None:
        psapi = ctypes.windll.psapi
    if ntdll is None:
        try:
            ntdll = ctypes.windll.ntdll
        except Exception:
            ntdll = None
    types = _win32_type_map()
    for name, (restype, argtypes) in WIN32_SIGNATURES.items():
        if name == "GetProcessMemoryInfo":
            dll = psapi
        elif name == "NtQueryInformationProcess":
            dll = ntdll
        else:
            dll = kernel32
        if dll is None:
            continue
        func = getattr(dll, name)
        func.restype = types[restype]
        func.argtypes = [types[a] for a in argtypes]
    return kernel32


def _kernel32():
    return configure_win32()


JOB_OBJECT_BASIC_ACCOUNTING_INFORMATION_CLASS = 1  # JobObjectBasicAccountingInformation
JOB_CPU_SCOPE = ("PMS owned job tree including exited children; cumulative "
                 "Job Object accounting since PMS assignment to the job, "
                 "wave delta covers setup, delivery, and session cleanup "
                 "inside the wave (not output verification or PMS stop)")


class JOBOBJECT_BASIC_ACCOUNTING_INFORMATION(ctypes.Structure):
    """Fixed-width JOBOBJECT_BASIC_ACCOUNTING_INFORMATION (48 bytes on Win64).

    LARGE_INTEGER counters use c_int64 and DWORD counters c_uint32 so the
    layout is 48 bytes on any host, not just where c_ulong is 4 bytes.
    Times are 100ns intervals, cumulative for every process ever assigned
    to the job, including processes that have already exited.
    """
    _fields_ = [("TotalUserTime", ctypes.c_int64),
                ("TotalKernelTime", ctypes.c_int64),
                ("ThisPeriodTotalUserTime", ctypes.c_int64),
                ("ThisPeriodTotalKernelTime", ctypes.c_int64),
                ("TotalPageFaultCount", ctypes.c_uint32),
                ("TotalProcesses", ctypes.c_uint32),
                ("ActiveProcesses", ctypes.c_uint32),
                ("TotalTerminatedProcesses", ctypes.c_uint32)]


def _job_accounting_unavailable(reason: str) -> dict:
    return {"available": False, "reason": reason, "scope": JOB_CPU_SCOPE,
            "user_cpu_s": None, "kernel_cpu_s": None, "total_cpu_s": None,
            "total_processes": None, "active_processes": None,
            "total_terminated_processes": None}


def query_job_accounting(job, kernel32=None) -> dict:
    """Cumulative Job Object CPU accounting for a borrowed job handle.

    Returns user/kernel/total CPU seconds plus total (ever assigned,
    including exited) and active process counts. On any failure every
    numeric field is None with ``available=False`` and a reason; zeros are
    never fabricated. The handle is borrowed: it is never closed here, so
    no handle/PID is leaked or released early.
    Peak committed memory is deliberately omitted: no queried field here
    has verified commit (not RSS) semantics for this use.
    """
    if not job:
        return _job_accounting_unavailable("no job handle")
    try:
        k32 = kernel32 if kernel32 is not None else _kernel32()
        info = JOBOBJECT_BASIC_ACCOUNTING_INFORMATION()
        ret_len = ctypes.c_ulong(0)
        ok = k32.QueryInformationJobObject(
            job, JOB_OBJECT_BASIC_ACCOUNTING_INFORMATION_CLASS,
            ctypes.byref(info), ctypes.sizeof(info), ctypes.byref(ret_len))
        if not ok:
            return _job_accounting_unavailable(
                f"QueryInformationJobObject failed (err {_last_error()})")
    except Exception as exc:  # noqa: BLE001 - reported, never fabricated
        return _job_accounting_unavailable(f"QueryInformationJobObject unavailable: {exc}")
    user_s = info.TotalUserTime / 1e7
    kernel_s = info.TotalKernelTime / 1e7
    return {"available": True, "reason": None, "scope": JOB_CPU_SCOPE,
            "user_cpu_s": round(user_s, 6), "kernel_cpu_s": round(kernel_s, 6),
            "total_cpu_s": round(user_s + kernel_s, 6),
            "total_processes": int(info.TotalProcesses),
            "active_processes": int(info.ActiveProcesses),
            "total_terminated_processes": int(info.TotalTerminatedProcesses)}


def compute_job_delta(start, end) -> dict:
    """Wave Job Object CPU delta; null (never negative/zero-faked) if invalid."""
    unavailable = {"job_cpu_delta_s": None, "job_user_cpu_delta_s": None,
                   "job_kernel_cpu_delta_s": None, "job_cpu_sampling": "unavailable",
                   "job_cpu_sampling_reason": None, "job_cpu_scope": JOB_CPU_SCOPE}
    for label, snap in (("start", start), ("end", end)):
        if not snap or snap.get("available") is not True:
            reason = (snap or {}).get("reason") or "missing accounting snapshot"
            unavailable["job_cpu_sampling_reason"] = f"{label} job accounting unavailable: {reason}"
            return unavailable
        if snap.get("total_cpu_s") is None:
            unavailable["job_cpu_sampling_reason"] = f"{label} job accounting has no CPU total"
            return unavailable
    total = end["total_cpu_s"] - start["total_cpu_s"]
    user = end["user_cpu_s"] - start["user_cpu_s"]
    kernel = end["kernel_cpu_s"] - start["kernel_cpu_s"]
    if total < 0 or user < 0 or kernel < 0:
        unavailable["job_cpu_sampling_reason"] = "negative job CPU delta (counters reset or job recreated)"
        return unavailable
    return {"job_cpu_delta_s": round(total, 3), "job_user_cpu_delta_s": round(user, 3),
            "job_kernel_cpu_delta_s": round(kernel, 3), "job_cpu_sampling": "available",
            "job_cpu_sampling_reason": None, "job_cpu_scope": JOB_CPU_SCOPE}


def _snapshot():
    """Return (ppid_map, name_map) via Toolhelp32 snapshot."""
    class PE32(ctypes.Structure):
        _fields_ = [("dwSize", ctypes.c_ulong), ("cntUsage", ctypes.c_ulong),
                    ("th32ProcessID", ctypes.c_ulong), ("th32DefaultHeapID", ctypes.c_void_p),
                    ("th32ModuleID", ctypes.c_ulong), ("cntThreads", ctypes.c_ulong),
                    ("th32ParentProcessID", ctypes.c_ulong), ("pcPriClassBase", ctypes.c_long),
                    ("dwFlags", ctypes.c_ulong), ("szExeFile", ctypes.c_wchar * 260)]
    k32 = _kernel32()
    snap = k32.CreateToolhelp32Snapshot(0x2, 0)  # TH32CS_SNAPPROCESS
    if not snap or snap == ctypes.c_void_p(-1).value:
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
    """(cpu_seconds, rss_bytes) or None when the PID is inaccessible/unreadable.
    Never fabricates 0/0 for an inaccessible process."""
    k32 = _kernel32()
    handle = k32.OpenProcess(0x1000, False, pid)  # QUERY_LIMITED_INFORMATION
    if not handle:
        return None
    try:
        class FT(ctypes.Structure):
            _fields_ = [("lo", ctypes.c_ulong), ("hi", ctypes.c_ulong)]
            def seconds(self): return ((self.hi << 32) | self.lo) / 1e7
        c, e, kern, user = FT(), FT(), FT(), FT()
        if not k32.GetProcessTimes(handle, ctypes.byref(c), ctypes.byref(e),
                                   ctypes.byref(kern), ctypes.byref(user)):
            return None
        cpu = kern.seconds() + user.seconds()
        rss = None
        class PMC(ctypes.Structure):
            _fields_ = [("cb", ctypes.c_ulong), ("PageFaultCount", ctypes.c_ulong),
                        ("PeakWorkingSetSize", ctypes.c_size_t), ("WorkingSetSize", ctypes.c_size_t),
                        ("q1", ctypes.c_size_t), ("q2", ctypes.c_size_t), ("q3", ctypes.c_size_t),
                        ("q4", ctypes.c_size_t), ("q5", ctypes.c_size_t), ("q6", ctypes.c_size_t)]
        counters = PMC()
        counters.cb = ctypes.sizeof(PMC)
        configure_win32()
        if not ctypes.windll.psapi.GetProcessMemoryInfo(handle, ctypes.byref(counters), counters.cb):
            return None
        rss = int(counters.WorkingSetSize)
        return cpu, rss
    finally:
        k32.CloseHandle(handle)


def sample_tree_win(root_pid: int) -> dict:
    """Tree sample. IO is NOT sampled on Windows (null, never invented zero);
    any inaccessible PID marks the whole sample unavailable."""
    ppid_map, name_map = _snapshot()
    total_cpu, total_rss, procs = 0.0, 0, []
    inaccessible = []
    for pid in build_process_tree(ppid_map, root_pid):
        measured = _proc_cpu_rss(pid)
        if measured is None:
            inaccessible.append(pid)
            procs.append({"pid": pid, "comm": name_map.get(pid, ""), "cpu_seconds": None,
                          "rss_bytes": None, "read_bytes": None, "write_bytes": None})
            continue
        cpu, rss = measured
        total_cpu += cpu
        total_rss += rss
        procs.append({"pid": pid, "comm": name_map.get(pid, ""), "cpu_seconds": round(cpu, 4),
                      "rss_bytes": rss, "read_bytes": None, "write_bytes": None})
    available = not inaccessible
    return {"cpu_seconds": total_cpu if available else None, "rss_bytes": total_rss if available else None,
            "read_bytes": None, "write_bytes": None, "processes": procs,
            "sampling_available": available, "inaccessible_pids": inaccessible}


def _proc_creation_time(pid: int):
    """FILETIME creation stamp for pid, or None. Guards against PID reuse."""
    try:
        k32 = _kernel32()
        handle = k32.OpenProcess(0x1000, False, pid)
        if not handle:
            return None
        try:
            class FT(ctypes.Structure):
                _fields_ = [("lo", ctypes.c_ulong), ("hi", ctypes.c_ulong)]
            c, e, kern, user = FT(), FT(), FT(), FT()
            if not k32.GetProcessTimes(handle, ctypes.byref(c), ctypes.byref(e),
                                       ctypes.byref(kern), ctypes.byref(user)):
                return None
            return (int(c.hi) << 32) | int(c.lo)
        finally:
            k32.CloseHandle(handle)
    except Exception:
        return None


def _read_remote(handle, address, size):
    if not address or not size:
        return None
    buf = ctypes.create_string_buffer(size)
    read = ctypes.c_size_t(0)
    if not _kernel32().ReadProcessMemory(handle, ctypes.c_void_p(address),
                                         buf, size, ctypes.byref(read)):
        return None
    if read.value != size:
        return None
    return buf.raw


def _read_process_cmdline(pid: int):
    """Command line via NtQueryInformationProcess->PEB (no subprocess).

    Returns the raw command-line string, or None when unavailable.
    Pointer-safe: all handle/address values travel as c_void_p/c_size_t.
    """
    if sys.platform != "win32":
        return None
    try:
        k32 = _kernel32()
        ntdll = ctypes.windll.ntdll
        configure_win32()
    except Exception:
        return None
    # PROCESS_QUERY_INFORMATION (0x0400) | PROCESS_VM_READ (0x0010)
    handle = k32.OpenProcess(0x0410, False, pid)
    if not handle:
        return None
    try:
        class PBI(ctypes.Structure):
            _fields_ = [("ExitStatus", ctypes.c_void_p), ("PebBaseAddress", ctypes.c_void_p),
                        ("AffinityMask", ctypes.c_void_p), ("BasePriority", ctypes.c_long),
                        ("UniqueProcessId", ctypes.c_void_p),
                        ("InheritedFromUniqueProcessId", ctypes.c_void_p)]
        pbi = PBI()
        ret_len = ctypes.c_ulong(0)
        status = ntdll.NtQueryInformationProcess(handle, 0, ctypes.byref(pbi),
                                                 ctypes.sizeof(pbi), ctypes.byref(ret_len))
        if status != 0 or not pbi.PebBaseAddress:
            return None
        is64 = ctypes.sizeof(ctypes.c_void_p) == 8
        params_off = 0x20 if is64 else 0x10
        cmdline_off = 0x70 if is64 else 0x40
        raw = _read_remote(handle, int(pbi.PebBaseAddress) + params_off,
                           ctypes.sizeof(ctypes.c_void_p))
        if not raw:
            return None
        params_addr = int.from_bytes(raw, "little")
        if not params_addr:
            return None
        # UNICODE_STRING: Length(u16), MaximumLength(u16), [pad], Buffer(ptr)
        us_size = 16 if is64 else 8
        raw = _read_remote(handle, params_addr + cmdline_off, us_size)
        if not raw:
            return None
        length = int.from_bytes(raw[0:2], "little")
        if length == 0 or length > 65534 or (length % 2):
            return None
        buffer_addr = int.from_bytes(raw[8:16] if is64 else raw[4:8], "little")
        data = _read_remote(handle, buffer_addr, length)
        if not data:
            return None
        return data.decode("utf-16-le", errors="replace")
    except Exception:
        return None
    finally:
        k32.CloseHandle(handle)


def capture_transcoder_cmdlines_win(root_pid: int, cmdline_reader=None) -> list:
    """Authentic argv from owned Transcoder descendants via native query.

    Each entry is verified against the process creation stamp before and
    after the read, so a reused PID never supplies a stale argv.
    """
    reader = cmdline_reader or _read_process_cmdline
    identity = _proc_creation_time if sys.platform == "win32" else None
    ppid_map, name_map = _snapshot()
    pids = [p for p in build_process_tree(ppid_map, root_pid)
            if "Transcoder" in name_map.get(p, "")]
    out = []
    for pid in pids:
        before = identity(pid) if identity else None
        try:
            cmdline = reader(pid) or ""
        except Exception:
            cmdline = ""
        cmdline = cmdline.strip() if isinstance(cmdline, str) else ""
        if identity is not None:
            after = identity(pid)
            if before is None or after != before:
                continue  # PID gone or reused between snapshot and read
        if not cmdline:
            continue
        out.append({"pid": pid, "process_creation_time": before,
                    "argv": [pb.sanitize(a) for a in cmdline.split()],
                    "cmdline": pb.sanitize(cmdline)})
    return out


def compute_tree_delta(start, end) -> dict:
    """Honest CPU delta (ba985258 mirror): invalid/incomplete -> nulls, never 0/0."""
    unavailable = {"tree_cpu_delta_s": None, "tree_io_delta": None, "io_sampling": "unavailable",
                   "cpu_sampling": "unavailable",
                   "cpu_sampling_reason": "invalid/incomplete tree delta (no root pid, "
                                          "no samples, or children exited dropping counters)"}
    if (not start or not end or not end.get("processes")
            or start.get("sampling_available") is False or end.get("sampling_available") is False
            or start.get("cpu_seconds") is None or end.get("cpu_seconds") is None):
        return unavailable
    # Snapshot sums only cover PIDs alive at each sample: when the PID sets
    # differ, exited children drop their counters and new children add
    # unrelated ones, so even a positive delta is incomplete/unreliable.
    start_pids = {p.get("pid") for p in start.get("processes") or []}
    end_pids = {p.get("pid") for p in end.get("processes") or []}
    if start_pids != end_pids:
        marked = dict(unavailable)
        marked["cpu_sampling_reason"] = (
            "snapshot delta incomplete/unreliable: PID identities differ "
            f"between samples (start={sorted(p for p in start_pids if p is not None)}, "
            f"end={sorted(p for p in end_pids if p is not None)}); exited "
            "children's CPU is missing - use Job Object accounting instead")
        marked["snapshot_delta_reliable"] = False
        return marked
    cpu = end["cpu_seconds"] - start["cpu_seconds"]
    if cpu < 0:
        return unavailable
    io_delta, io_sampling = None, "unavailable"  # Windows driver does not sample IO
    if None not in (start.get("read_bytes"), start.get("write_bytes"),
                    end.get("read_bytes"), end.get("write_bytes")):
        read_b = end["read_bytes"] - start["read_bytes"]
        write_b = end["write_bytes"] - start["write_bytes"]
        if read_b < 0 or write_b < 0:
            return unavailable
        io_delta, io_sampling = {"read_bytes": read_b, "write_bytes": write_b}, "available"
    return {"tree_cpu_delta_s": round(cpu, 3), "tree_io_delta": io_delta,
            "io_sampling": io_sampling,
            "cpu_sampling": "available", "cpu_sampling_reason": None,
            "snapshot_delta_reliable": True}


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
        last_delivery = None
        downloaded, seen = {}, set()
        ordinals = {}  # uri -> stable playlist ordinal
        peak_rss = None
        samples = []
        argv_by_key = {}
        lock = threading.Lock()
        argv_stop = threading.Event()
        poller = None
        cpu_start = None
        measurement_end = None
        cleanup_s = 0.0
        poller_alive = False
        end = False
        segs, total = [], 0.0
        playlist_url = None
        start_url = f"{cfg.base_url}/video/:/transcode/universal/start.m3u8?{qs}"

        def argv_poller():
            while not argv_stop.is_set():
                if root_pid:
                    try:
                        found = capture_transcoder_cmdlines_win(root_pid)
                    except Exception:
                        found = []
                    if found:
                        with lock:
                            for entry in found:
                                command = entry.get("cmdline", "")
                                if "/video/:/transcode/session/" in command and f"/{session}/" not in command:
                                    continue
                                key = (entry.get("pid"),
                                       entry.get("process_creation_time"),
                                       entry.get("cmdline"))
                                argv_by_key[key] = entry
                argv_stop.wait(0.05)

        def fetch(item):
            nonlocal first_latency, last_delivery
            uri, extinf = item
            # PMS lists segments before they are fully written; retry 404s briefly.
            seg_deadline = time.monotonic() + 15.0
            while True:
                try:
                    st, _, data = pb.http_get(uri, timeout=min(30, cfg.timeout_s),
                                              headers=headers)
                    break
                except pb.BenchError as exc:
                    if "HTTP 404" in str(exc) and time.monotonic() < seg_deadline:
                        time.sleep(0.5)
                        continue
                    raise
            if st != 200:
                raise pb.BenchError(f"segment HTTP {st}")
            if not data:
                raise pb.BenchError(f"empty segment {pb.sanitize(uri)}")
            with lock:
                last_delivery = time.monotonic()
                downloaded[uri] = (len(data), extinf)
                ordinal = ordinals.get(uri, 0)
                if ordinal == 0 and first_latency is None:
                    first_latency = last_delivery - t_enc
            if cfg.preserve_media:
                (out_dir / f"session-{idx}" / f"seg-{ordinals.get(uri, 0):05d}.ts").write_bytes(data)
            return len(data)

        try:
            # CPU baseline and argv poller start BEFORE start/playlist so
            # encoder startup is inside the measured window.
            cpu_start = sample_tree_win(root_pid) if root_pid else None
            poller = threading.Thread(target=argv_poller, daemon=True,
                                      name=f"plex-bench-argv-{idx}")
            poller.start()
            status, _, body = pb.http_get(start_url, timeout=min(30, cfg.timeout_s),
                                          headers=headers)
            if status != 200:
                raise pb.BenchError(f"start.m3u8 HTTP {status}")
            is_master, media_url, segs, total, end = pb.parse_playlist(
                body.decode(errors="replace"), start_url)
            if is_master:
                status, _, body = pb.http_get(media_url, timeout=min(30, cfg.timeout_s),
                                              headers=headers)
                if status != 200:
                    raise pb.BenchError(f"media playlist HTTP {status}")
                _, _, segs, total, end = pb.parse_playlist(
                    body.decode(errors="replace"), media_url)
                playlist_url = media_url
            else:
                playlist_url = start_url
            deadline = t_enc + cfg.timeout_s
            media_dir = out_dir / f"session-{idx}"
            if cfg.preserve_media:
                media_dir.mkdir(parents=True, exist_ok=True)
            while time.monotonic() < deadline:
                try:
                    status, _, body = pb.http_get(playlist_url,
                                                  timeout=min(15, cfg.timeout_s),
                                                  headers=headers)
                    if status != 200:
                        raise pb.BenchError(f"playlist HTTP {status}")
                    _, _, segs, total, end = pb.parse_playlist(
                        body.decode(errors="replace"), playlist_url)
                except pb.BenchError:
                    if downloaded:
                        raise
                    time.sleep(cfg.poll_interval_s)
                    continue
                for ordinal, (uri, _ext) in enumerate(segs):
                    ordinals.setdefault(uri, ordinal)
                queued_s = sum(v[1] for v in downloaded.values())
                new = []
                for item in segs:
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
                            delivered_s = sum(v[1] for v in downloaded.values())
                            if delivered_s < cfg.duration_s:
                                # Pace the next request at the cumulative
                                # EXTINF boundary; never sleep after the
                                # final delivered segment.
                                wait_s = (t_enc + delivered_s) - time.monotonic()
                                if wait_s > 0:
                                    time.sleep(wait_s)
                    else:
                        # Preserve playback order. Concurrent distant requests
                        # can make PMS interpret forward reads as seeks and
                        # replace the encoder while earlier segments are read.
                        for item in new:
                            fetch(item)
                if root_pid:
                    smp = sample_tree_win(root_pid)
                    samples.append(smp)
                    rss = smp.get("rss_bytes") if isinstance(smp, dict) else None
                    if rss is not None:
                        peak_rss = rss if peak_rss is None else max(peak_rss, rss)
                actual_media = sum(v[1] for v in downloaded.values())
                # Linux parity: requested duration alone ends the session;
                # do not wait for ENDLIST / the whole source to finish.
                if actual_media >= cfg.duration_s:
                    break
                if end and not new and downloaded:
                    break
                time.sleep(cfg.poll_interval_s)
            else:
                raise pb.BenchError(f"session timeout after {cfg.timeout_s}s")
            # Measurement completes at last segment delivery, before any
            # poller join / final sampling instrumentation cleanup.
            measurement_end = last_delivery
        finally:
            cleanup_t0 = time.monotonic()
            argv_stop.set()
            if poller is not None:
                poller.join(timeout=5)
                poller_alive = poller.is_alive()
            cleanup_s = time.monotonic() - cleanup_t0
        if poller_alive:
            raise pb.BenchError("argv poller did not stop after session")
        cmdlines = list(argv_by_key.values())
        if root_pid and not cmdlines:
            raise pb.BenchError("no transcoder argv captured from owned PMS tree; "
                                "rejecting as non-authentic result")
        actual_media = sum(v[1] for v in downloaded.values())
        if not downloaded:
            raise pb.BenchError("no segments downloaded")
        if actual_media <= 0:
            raise pb.BenchError("unknown media duration: EXTINF total is zero")
        encode_s = measurement_end - t_enc
        cpu_t0 = time.monotonic()
        cpu_end = sample_tree_win(root_pid) if root_pid else None
        cleanup_s += time.monotonic() - cpu_t0
        result.update({"encode_s": encode_s,
                       "measurement_duration_s": encode_s,
                       "measurement_finished_monotonic_s": measurement_end,
                       "measurement_end_monotonic": measurement_end,
                       "measurement_complete_s": encode_s,
                       "cleanup_s": cleanup_s,
                       "cleanup_overhead_s": cleanup_s,
                       "instrumentation_cleanup_s": cleanup_s,
                       "first_segment_latency_s": first_latency,
                       "actual_media_s": round(actual_media, 3),
                       "segment_count": len(downloaded),
                       "output_bytes": sum(v[0] for v in downloaded.values()),
                       "peak_rss_bytes": peak_rss, "samples": samples[-20:],
                       "transcoder_cmdlines": cmdlines, "playlist_endlist": end})
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
            "transcoder_used_hw": pb.hardware_encode_verified(cmdlines)}
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


CREATE_SUSPENDED = 0x00000004
CREATE_NEW_PROCESS_GROUP = 0x00000200
CREATE_UNICODE_ENVIRONMENT = 0x00000400
LAUNCH_FLAGS = CREATE_SUSPENDED | CREATE_NEW_PROCESS_GROUP | CREATE_UNICODE_ENVIRONMENT


def _last_error() -> int:
    getter = getattr(ctypes, "get_last_error", None)
    return getter() if getter else -1


def build_env_block(env: dict) -> str:
    """UTF-16 environment block string: keys sorted case-insensitively,
    each entry NUL-terminated, whole block double-NUL terminated."""
    return "".join(f"{k}={env[k]}\0" for k in sorted(env, key=str.casefold)) + "\0"
JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x2000
STILL_ACTIVE = 259


class WinProcess:
    """Minimal process handle from CreateProcessW (suspended launch)."""

    def __init__(self, handle, thread_handle, pid, tid):
        self._handle = handle
        self.thread_handle = thread_handle
        self.pid = pid
        self.tid = tid
        self._returncode = None

    @property
    def returncode(self):
        return self.poll()

    def poll(self):
        if self._returncode is not None:
            return self._returncode
        k32 = _kernel32()
        code = ctypes.c_ulong()
        if not k32.GetExitCodeProcess(self._handle, ctypes.byref(code)):
            raise pb.BenchError(f"GetExitCodeProcess failed for pid {self.pid}")
        if code.value == STILL_ACTIVE:
            return None
        self._returncode = int(code.value)
        return self._returncode

    def wait(self, timeout=None):
        k32 = _kernel32()
        ms = 0xFFFFFFFF if timeout is None else int(timeout * 1000)
        result = k32.WaitForSingleObject(self._handle, ms)
        if result == 0x00000102:  # WAIT_TIMEOUT
            raise subprocess.TimeoutExpired(str(self.pid), timeout)
        if result == 0xFFFFFFFF:  # WAIT_FAILED: never silently poll/claim cleanup
            raise pb.BenchError(f"WaitForSingleObject failed for pid {self.pid} "
                                f"(err {_last_error()})")
        if result != 0:  # WAIT_OBJECT_0 expected; WAIT_ABANDONED etc are failures here
            raise pb.BenchError(f"WaitForSingleObject unexpected result {result:#x} "
                                f"for pid {self.pid}")
        return self.poll()

    def terminate(self):
        if self.poll() is None:
            if not _kernel32().TerminateProcess(self._handle, 1):
                raise pb.BenchError(f"TerminateProcess failed for pid {self.pid}")

    def resume(self):
        previous = _kernel32().ResumeThread(self.thread_handle)
        if previous == 0xFFFFFFFF:
            raise pb.BenchError(f"ResumeThread failed for pid {self.pid}")
        return previous

    def close_handles(self):
        k32 = _kernel32()
        for handle in (self.thread_handle, self._handle):
            if handle:
                k32.CloseHandle(handle)
        self.thread_handle = self._handle = None


def create_job_object():
    """Configured KILL_ON_JOB_CLOSE job handle, or raise (fail closed)."""
    k32 = _kernel32()

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
        raise pb.BenchError("CreateJobObjectW failed")
    info = JOBOBJECT_EXTENDED_LIMIT_INFORMATION()
    info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
    if not k32.SetInformationJobObject(job, 9, ctypes.byref(info), ctypes.sizeof(info)):
        k32.CloseHandle(job)
        raise pb.BenchError("SetInformationJobObject failed")
    return job


def launch_suspended(exe: Path, cwd: Path, env: dict, args=None) -> WinProcess:
    """CreateProcessW with CREATE_SUSPENDED so no child can escape the job.

    ``args`` (default None) is an optional list of extra argument strings
    appended after the executable, quoted with subprocess.list2cmdline so
    callers reusing the private job (e.g. an Ether harness root process)
    get safe quoting. With args=None the historical command line
    (``"<exe>"`` only) is preserved exactly.
    """
    k32 = _kernel32()

    class STARTUPINFOW(ctypes.Structure):
        _fields_ = [("cb", ctypes.c_ulong), ("lpReserved", ctypes.c_wchar_p),
                    ("lpDesktop", ctypes.c_wchar_p), ("lpTitle", ctypes.c_wchar_p),
                    ("dwX", ctypes.c_ulong), ("dwY", ctypes.c_ulong),
                    ("dwXSize", ctypes.c_ulong), ("dwYSize", ctypes.c_ulong),
                    ("dwXCountChars", ctypes.c_ulong), ("dwYCountChars", ctypes.c_ulong),
                    ("dwFillAttribute", ctypes.c_ulong), ("dwFlags", ctypes.c_ulong),
                    ("wShowWindow", ctypes.c_ushort), ("cbReserved2", ctypes.c_ushort),
                    ("lpReserved2", ctypes.c_void_p), ("hStdInput", ctypes.c_void_p),
                    ("hStdOutput", ctypes.c_void_p), ("hStdError", ctypes.c_void_p)]

    class PROCESS_INFORMATION(ctypes.Structure):
        _fields_ = [("hProcess", ctypes.c_void_p), ("hThread", ctypes.c_void_p),
                    ("dwProcessId", ctypes.c_ulong), ("dwThreadId", ctypes.c_ulong)]

    startup = STARTUPINFOW()
    startup.cb = ctypes.sizeof(STARTUPINFOW)
    proc_info = PROCESS_INFORMATION()
    cmdline = (f'"{exe}"' if args is None
               else subprocess.list2cmdline([str(exe)] + [str(a) for a in args]))
    command = ctypes.create_unicode_buffer(cmdline)
    env_block = ctypes.create_unicode_buffer(build_env_block(env))
    created = k32.CreateProcessW(None, command, None, None, False,
                                 LAUNCH_FLAGS,
                                 env_block, str(cwd), ctypes.byref(startup),
                                 ctypes.byref(proc_info))
    if not created:
        raise pb.BenchError(f"CreateProcessW failed for {exe} (err {_last_error()})")
    return WinProcess(proc_info.hProcess, proc_info.hThread,
                      int(proc_info.dwProcessId), int(proc_info.dwThreadId))


class PmsProcessWin:
    def __init__(self, cfg):
        self.cfg = cfg
        self.proc = None
        self.pid = None
        self.job = None

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
        self.job = create_job_object()
        try:
            self.proc = launch_suspended(exe, exe.parent, env)
            self.pid = self.proc.pid
            if not _kernel32().AssignProcessToJobObject(self.job, self.proc._handle):
                raise pb.BenchError("AssignProcessToJobObject failed (fail closed)")
            self.proc.resume()
        except Exception:
            # Fail closed: nothing suspended/running may survive a failed launch.
            try:
                if self.proc:
                    self.proc.terminate()
                    self.proc.wait(timeout=5)
            finally:
                if self.job:
                    _kernel32().CloseHandle(self.job)
                    self.job = None
                if self.proc:
                    self.proc.close_handles()
                    self.proc = None
                    self.pid = None
            raise
        return self.pid

    def job_accounting(self) -> dict:
        """Cumulative accounting for this PMS instance's private job."""
        return query_job_accounting(self.job)

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

    def stop(self) -> bool:
        """Stop the owned tree and VERIFY it. Raises BenchError on any failure;
        returns True only once the root is exited and no descendant survives."""
        if not self.proc:
            return True
        descendants = []
        ppid_map, _ = _snapshot()  # capture descendants BEFORE exit (leak check)
        descendants = build_process_tree(ppid_map, self.pid)
        errors = []
        if self.job:
            if not _kernel32().CloseHandle(self.job):  # KILL_ON_JOB_CLOSE
                errors.append("CloseHandle(job) failed")
            self.job = None
        try:
            if self.proc.poll() is None:
                self.proc.terminate()
        except Exception as exc:  # noqa: BLE001 - collected, then raised below
            errors.append(str(exc))
        try:
            self.proc.wait(timeout=10)
        except Exception as exc:  # noqa: BLE001
            errors.append(f"PMS root did not exit: {exc}")
            for pid in descendants:
                subprocess.run(["taskkill", "/PID", str(pid), "/F"],
                               capture_output=True, timeout=10)
            try:
                self.proc.wait(timeout=10)
            except Exception as exc2:  # noqa: BLE001
                errors.append(f"PMS root still alive after taskkill: {exc2}")
        survivors = []
        try:
            ppid_map, _ = _snapshot()
            alive = set(ppid_map)
            survivors = [pid for pid in descendants if pid in alive and pid != self.pid]
        except Exception as exc:  # noqa: BLE001
            errors.append(f"post-stop audit failed: {exc}")
        if survivors:
            errors.append(f"descendant processes survived stop: {survivors}")
        try:
            self.proc.close_handles()
        except Exception as exc:  # noqa: BLE001
            errors.append(str(exc))
        if errors:
            raise pb.BenchError("PMS stop failed: " + "; ".join(errors))
        return True


def main(argv=None):
    ap = argparse.ArgumentParser(description="Authentic native Windows Plex NVENC baseline")
    ap.add_argument("--lab-root", default=DEFAULT_LAB)
    ap.add_argument("--base-url", default="http://127.0.0.1:32400")
    ap.add_argument("--rating-key", default="1")
    ap.add_argument("--fixture", default="", help="Actual media file selected by rating-key, for reproducible source hashing")
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
    fixture = Path(args.fixture or plan["fixture"])
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
            job_start = query_job_accounting(pms.job if pms else None)
            wave_sessions = []
            with concurrent.futures.ThreadPoolExecutor(max_workers=cfg.concurrency) as ex:
                futs = [ex.submit(run_session_win, cfg, root_pid, out_dir,
                                  wave * cfg.concurrency + i) for i in range(cfg.concurrency)]
                for fut in futs:
                    wave_sessions.append(fut.result())
            # After wave completion, BEFORE any verification: cumulative
            # job counters still include encoders that already exited.
            job_end = query_job_accounting(pms.job if pms else None)
            wall_s = round(time.monotonic() - wave_start, 3)
            # Measurement wall: setup-inclusive, ends at last segment
            # delivery (max measurement_end), excluding poller cleanup.
            meas_ends = [s["measurement_finished_monotonic_s"] for s in wave_sessions
                         if s.get("measurement_finished_monotonic_s")]
            delivery_wall_s = (round(max(meas_ends) - wave_start, 3)
                               if meas_ends else wall_s)
            measurement_wall_s = delivery_wall_s
            cleanup_overhead_s = round(max(0.0, wall_s - delivery_wall_s), 3)
            media_total = round(sum(s["actual_media_s"] for s in wave_sessions), 3)
            waves.append({"wave": wave, "job_accounting_start": job_start,
                          "job_accounting_end": job_end,
                          **compute_job_delta(job_start, job_end),
                          "wall_s": wall_s,
                          "delivery_wall_s": delivery_wall_s,
                          "measurement_wall_s": measurement_wall_s,
                          "cleanup_s": cleanup_overhead_s,
                          "cleanup_overhead_s": cleanup_overhead_s,
                          "concurrency": cfg.concurrency,
                          "sessions": len(wave_sessions), "actual_media_total_s": media_total,
                          "aggregate_media_per_wall_s": round(media_total / wall_s, 3) if wall_s else None,
                          "aggregate_media_per_delivery_wall_s": (
                              round(media_total / delivery_wall_s, 3)
                              if delivery_wall_s else None),
                          "aggregate_media_per_measurement_wall_s": (
                              round(media_total / measurement_wall_s, 3)
                              if measurement_wall_s else None)})
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
    finally:
        if pms:
            try:
                meta["pms_stopped"] = pms.stop()  # raises unless verified stopped
            except Exception as exc:  # noqa: BLE001 - never claim stopped on failure
                meta["pms_stopped"] = False
                meta["ok"] = False
                meta["error"] = (meta.get("error") or "") + f" PMS stop failed: {pb.sanitize(str(exc))}"
    (out_dir / "result.json").write_text(json.dumps(meta, indent=2))
    if not meta.get("ok"):
        print(json.dumps({"ok": False, "error": meta.get("error", ""), "out": str(out_dir)}))
        return 1
    print(json.dumps({"ok": True, "out": str(out_dir), "sessions": len(sessions),
                      "actual_media_s": [s.get("actual_media_s") for s in sessions]}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
