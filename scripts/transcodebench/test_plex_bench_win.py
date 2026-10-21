#!/usr/bin/env python3
"""Offline tests for the native Windows driver adaptation. No Plex, no Windows required."""
import sys, unittest
import http.server
from pathlib import Path
sys.path.insert(0, str(Path(__file__).parent))
import plex_bench_win as win


class TestIsolationPlan(unittest.TestCase):
    def test_plan_is_d_drive_scoped(self):
        plan = win.isolation_plan(r"D:\EtherTranscodeLab\Plex")
        self.assertEqual(plan["pms_exe"], r"D:\EtherTranscodeLab\Plex\app\Plex Media Server.exe")
        self.assertEqual(plan["local_app_data_path"], r"D:\EtherTranscodeLab\Plex\data")
        self.assertEqual(plan["expected_data_dir"], r"D:\EtherTranscodeLab\Plex\data\Plex Media Server")
        self.assertEqual(plan["registry_value"], "LocalAppDataPath")
        self.assertTrue(plan["registry_key"].endswith(r"Software\Plex, Inc.\Plex Media Server"))
        for key in ("app_dir", "transcode_dir", "media_dir", "results_dir", "fixture"):
            self.assertTrue(plan[key].startswith("D:\\"), f"{key} escapes lab: {plan[key]}")

    def test_plan_normalizes_trailing_slash(self):
        plan = win.isolation_plan("D:\\EtherTranscodeLab\\Plex\\")
        self.assertEqual(plan["lab_root"], r"D:\EtherTranscodeLab\Plex")


class TestProfileExtra(unittest.TestCase):
    EXPECTED = (
        "add-transcode-target(type=videoProfile&context=streaming&protocol=hls"
        "&container=mpegts&videoCodec=h264&audioCodec=aac&replace=true)"
        "+add-limitation(scope=videoCodec&scopeName=h264&type=upperBound&name=video.width&value=640)"
        "+add-limitation(scope=videoCodec&scopeName=h264&type=upperBound&name=video.height&value=360)")

    def test_exact_profile_extra(self):
        self.assertEqual(win.profile_extra("h264", "aac", "640x360"), self.EXPECTED)

    def test_headers_use_android_not_generic(self):
        import plex_bench as pb
        headers = win.plex_headers(pb.Profile(platform="Android"))
        self.assertEqual(headers["X-Plex-Platform"], "Android")
        self.assertEqual(headers["X-Plex-Client-Profile-Extra"], self.EXPECTED)

    def test_gate_accepts_1001_rejects_2000_and_none(self):
        import xml.etree.ElementTree as ET
        import plex_bench as pb
        ok = ET.fromstring('<MediaContainer generalDecisionCode="1001" transcodeDecisionCode="1001"/>')
        self.assertEqual(win.check_decision_gate(ok)["generalDecisionCode"], "1001")
        bad = ET.fromstring('<MediaContainer generalDecisionCode="2000" transcodeDecisionCode="4005"/>')
        with self.assertRaises(pb.BenchError):
            win.check_decision_gate(bad)
        none = ET.fromstring('<MediaContainer generalDecisionCode="1000"><Media decision="none"/></MediaContainer>')
        with self.assertRaises(pb.BenchError):
            win.check_decision_gate(none)


class TestHonesty(unittest.TestCase):
    def test_cpu_delta_null_when_no_root(self):
        out = win.compute_tree_delta(None, None)
        self.assertIsNone(out["tree_cpu_delta_s"])
        self.assertEqual(out["cpu_sampling"], "unavailable")

    def test_cpu_delta_null_when_children_exited(self):
        start = {"cpu_seconds": 10.0, "read_bytes": 0, "write_bytes": 0, "processes": [{"pid": 1}]}
        end = {"cpu_seconds": 2.0, "read_bytes": 0, "write_bytes": 0, "processes": [{"pid": 1}]}
        self.assertIsNone(win.compute_tree_delta(start, end)["tree_cpu_delta_s"])

    def test_cpu_delta_valid(self):
        start = {"cpu_seconds": 1.0, "read_bytes": 0, "write_bytes": 0, "processes": [{"pid": 1}]}
        end = {"cpu_seconds": 3.5, "read_bytes": 10, "write_bytes": 20, "processes": [{"pid": 1}]}
        out = win.compute_tree_delta(start, end)
        self.assertEqual(out["tree_cpu_delta_s"], 2.5)
        self.assertEqual(out["cpu_sampling"], "available")

    def test_verify_all_sessions_missing_is_unchecked_not_skipped(self):
        import tempfile
        import plex_bench as pb
        cfg = pb.Config(lab_root="/nonexistent", ffprobe="/nonexistent/ffprobe")
        with tempfile.TemporaryDirectory() as d:
            out = win.verify_output_win(cfg, Path(d), [{"session_index": 0}, {"session_index": 1}])
        self.assertFalse(out["checked"])
        self.assertEqual(len(out["sessions"]), 2)
        self.assertFalse(out["sessions"][1]["checked"])


class TestWin32Safety(unittest.TestCase):
    HANDLE_APIS = ("CreateToolhelp32Snapshot", "OpenProcess", "OpenThread",
                   "CreateJobObjectW")

    def test_handle_apis_are_pointer_safe_not_default_int(self):
        for name in self.HANDLE_APIS:
            self.assertEqual(win.WIN32_SIGNATURES[name][0], "HANDLE",
                             f"{name} restype must be HANDLE")
        self.assertEqual(win.WIN32_SIGNATURES["CreateProcessW"][0], "BOOL")
        for name in ("CloseHandle", "GetProcessTimes", "AssignProcessToJobObject",
                     "SetInformationJobObject", "ResumeThread", "WaitForSingleObject"):
            self.assertIn(name, win.WIN32_SIGNATURES)

    def test_configure_applies_signatures_to_dlls(self):
        import ctypes
        from unittest import mock
        kernel, psapi = mock.MagicMock(), mock.MagicMock()
        funcs = {}
        for name in win.WIN32_SIGNATURES:
            func = mock.MagicMock()
            funcs[name] = func
            setattr(psapi if name == "GetProcessMemoryInfo" else kernel, name, func)
        win.configure_win32(kernel32=kernel, psapi=psapi)
        self.assertIs(funcs["CreateToolhelp32Snapshot"].restype, ctypes.c_void_p)
        self.assertIs(funcs["OpenProcess"].restype, ctypes.c_void_p)
        self.assertIs(funcs["CreateJobObjectW"].restype, ctypes.c_void_p)

    def test_suspended_launch_assigns_job_before_resume(self):
        import inspect
        src = inspect.getsource(win.launch_suspended) + inspect.getsource(win.PmsProcessWin.start)
        self.assertIn("CREATE_SUSPENDED", src)
        self.assertLess(src.index("AssignProcessToJobObject"), src.index(".resume()"))

    def test_inaccessible_pid_marks_sample_unavailable(self):
        from unittest import mock
        with mock.patch.object(win, "_snapshot", return_value=({1: 0, 2: 1}, {1: "pms", 2: "child"})), \
             mock.patch.object(win, "_proc_cpu_rss", side_effect=[(1.0, 100), None]):
            sample = win.sample_tree_win(1)
        self.assertFalse(sample["sampling_available"])
        self.assertIsNone(sample["cpu_seconds"])
        self.assertEqual(sample["inaccessible_pids"], [2])
        delta = win.compute_tree_delta(sample, sample)
        self.assertIsNone(delta["tree_cpu_delta_s"])
        self.assertEqual(delta["cpu_sampling"], "unavailable")

    def test_io_is_null_not_invented_zero(self):
        from unittest import mock
        with mock.patch.object(win, "_snapshot", return_value=({1: 0}, {1: "pms"})), \
             mock.patch.object(win, "_proc_cpu_rss", return_value=(2.0, 100)):
            sample = win.sample_tree_win(1)
        self.assertIsNone(sample["read_bytes"])
        start = dict(sample, cpu_seconds=1.0)
        end = dict(sample, cpu_seconds=3.0)
        delta = win.compute_tree_delta(start, end)
        self.assertEqual(delta["tree_cpu_delta_s"], 2.0)
        self.assertIsNone(delta["tree_io_delta"])
        self.assertEqual(delta["io_sampling"], "unavailable")


class TestLaunchFlags(unittest.TestCase):
    def test_exact_launch_flags(self):
        self.assertEqual(win.CREATE_SUSPENDED, 0x00000004)
        self.assertEqual(win.CREATE_NEW_PROCESS_GROUP, 0x00000200)
        self.assertEqual(win.CREATE_UNICODE_ENVIRONMENT, 0x00000400)
        self.assertEqual(win.LAUNCH_FLAGS, 0x00000604)
        import inspect
        self.assertIn("LAUNCH_FLAGS", inspect.getsource(win.launch_suspended))

    def test_env_block_sorted_case_insensitive_double_nul(self):
        block = win.build_env_block({"Zebra": "1", "apple": "2", "Banana": "3"})
        self.assertEqual(block, "apple=2\0Banana=3\0Zebra=1\0\0")

    def test_wait_failed_raises_not_poll(self):
        from unittest import mock
        import plex_bench as pb
        fake = mock.MagicMock()
        fake.WaitForSingleObject.return_value = 0xFFFFFFFF
        proc = win.WinProcess(handle=1, thread_handle=2, pid=99, tid=1)
        with mock.patch.object(win, "_kernel32", return_value=fake):
            with self.assertRaises(pb.BenchError):
                proc.wait(timeout=1)
        fake.GetExitCodeProcess.assert_not_called()

    def test_wait_timeout_still_timeout(self):
        from unittest import mock
        import subprocess as sp
        fake = mock.MagicMock()
        fake.WaitForSingleObject.return_value = 0x00000102
        proc = win.WinProcess(handle=1, thread_handle=2, pid=99, tid=1)
        with mock.patch.object(win, "_kernel32", return_value=fake):
            with self.assertRaises(sp.TimeoutExpired):
                proc.wait(timeout=1)


class _FakePlexHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _send(self, code, body, ctype="text/plain"):
        data = body.encode() if isinstance(body, str) else body
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        import urllib.parse as up
        st = self.server.state
        path = up.urlparse(self.path).path
        if path.endswith("/decision"):
            self._send(200, '<MediaContainer generalDecisionCode="1000" '
                            'transcodeDecisionCode="1001">'
                            '<Media videoCodec="h264" width="640" height="360" '
                            'videoResolution="640x360"/></MediaContainer>',
                       "text/xml")
        elif path.endswith("/stop"):
            self._send(200, "ok")
        elif path.endswith("/start.m3u8"):
            endlist = "#EXT-X-ENDLIST\n" if st.get("endlist") else ""
            self._send(200, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n"
                            "#EXTINF:4.0,\n/seg0.ts\n#EXTINF:4.0,\n/seg1.ts\n" + endlist)
        elif path in ("/seg0.ts", "/seg1.ts"):
            st["seg_attempts"][path] = st["seg_attempts"].get(path, 0) + 1
            if st.get("seg_fail"):
                self._send(500, "boom")
            elif st["seg_attempts"][path] <= st.get("seg_404s", {}).get(path, 0):
                self._send(404, "not ready")
            else:
                self._send(200, b"\x47" * 188)
        else:
            self._send(404, "nope")


def _fake_cfg(base_url, **kw):
    import plex_bench as pb
    args = dict(lab_root="/tmp", base_url=base_url, rating_key="1",
                duration_s=8.0, timeout_s=10.0, poll_interval_s=0.02)
    args.update(kw)
    return pb.Config(**args)


import contextlib


@contextlib.contextmanager
def _fake_server(state=None):
    import http.server
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), _FakePlexHandler)
    srv.state = {"seg_attempts": {}, "seg_404s": {}, "endlist": False,
                 "seg_fail": False}
    if state:
        srv.state.update(state)
    t = __import__("threading").Thread(target=srv.serve_forever, daemon=True)
    t.start()
    try:
        yield srv, f"http://127.0.0.1:{srv.server_address[1]}"
    finally:
        srv.shutdown()
        srv.server_close()
        t.join(timeout=5)


class TestFakeServerSessions(unittest.TestCase):
    def _sample(self, rss):
        return {"cpu_seconds": 1.0, "rss_bytes": rss, "read_bytes": None,
                "write_bytes": None, "processes": [{"pid": 1}],
                "sampling_available": rss is not None, "inaccessible_pids": []}

    def test_unfinished_playlist_breaks_at_duration(self):
        import tempfile, time
        from pathlib import Path as P
        with _fake_server() as (srv, base), tempfile.TemporaryDirectory() as d:
            t0 = time.monotonic()
            out = win.run_session_win(_fake_cfg(base), None, P(d), 0)
            elapsed = time.monotonic() - t0
        self.assertEqual(out["actual_media_s"], 8.0)
        self.assertFalse(out["playlist_endlist"])
        self.assertEqual(out["segment_count"], 2)
        self.assertLess(elapsed, 5.0)
        self.assertLess(out["encode_s"], 5.0)

    def test_paced_waits_cumulative_extinf_no_sleep_after_final(self):
        import tempfile, time
        from pathlib import Path as P
        with _fake_server() as (srv, base), tempfile.TemporaryDirectory() as d:
            t0 = time.monotonic()
            out = win.run_session_win(_fake_cfg(base, mode="paced"), None, P(d), 0)
            elapsed = time.monotonic() - t0
        # First 4s segment paces the next request to ~4s; the final
        # segment adds no sleep, so total is ~4s, not ~6s (2s cap x2)
        # and not ~8s (sleep after final).
        self.assertEqual(out["actual_media_s"], 8.0)
        self.assertGreaterEqual(elapsed, 3.5)
        self.assertLess(elapsed, 6.5)

    def test_404_retry_while_segment_written(self):
        import tempfile
        from pathlib import Path as P
        with _fake_server({"seg_404s": {"/seg0.ts": 2}}) as (srv, base), \
                tempfile.TemporaryDirectory() as d:
            out = win.run_session_win(_fake_cfg(base), None, P(d), 0)
        self.assertEqual(out["segment_count"], 2)
        self.assertGreaterEqual(srv.state["seg_attempts"]["/seg0.ts"], 3)

    def test_unavailable_rss_is_none_not_fake_zero(self):
        import tempfile
        from pathlib import Path as P
        from unittest import mock
        argv = [{"pid": 9, "process_creation_time": 1,
                 "argv": ["Plex Transcoder"], "cmdline": "Plex Transcoder -nvenc"}]
        with _fake_server() as (srv, base), tempfile.TemporaryDirectory() as d, \
                mock.patch.object(win, "sample_tree_win",
                                  return_value=self._sample(None)), \
                mock.patch.object(win, "capture_transcoder_cmdlines_win",
                                  return_value=argv):
            out = win.run_session_win(_fake_cfg(base), 123, P(d), 0)
        self.assertIsNone(out["peak_rss_bytes"])

    def test_delayed_argv_capture_not_in_encode_s(self):
        import tempfile, time
        from pathlib import Path as P
        from unittest import mock

        def slow_capture(pid):
            time.sleep(0.6)
            return [{"pid": 9, "process_creation_time": 1,
                     "argv": ["Plex Transcoder"],
                     "cmdline": "Plex Transcoder -nvenc"}]

        with _fake_server() as (srv, base), tempfile.TemporaryDirectory() as d, \
                mock.patch.object(win, "sample_tree_win",
                                  return_value=self._sample(100)), \
                mock.patch.object(win, "capture_transcoder_cmdlines_win",
                                  side_effect=slow_capture):
            out = win.run_session_win(_fake_cfg(base), 123, P(d), 0)
        self.assertLess(out["encode_s"], 0.45)
        self.assertEqual(out["measurement_complete_s"], out["encode_s"])
        self.assertGreaterEqual(out["cleanup_s"], 0.3)
        self.assertIn("measurement_finished_monotonic_s", out)
        self.assertEqual(out["measurement_finished_monotonic_s"],
                         out["measurement_end_monotonic"])

    def test_exception_stops_and_reaps_poller(self):
        import tempfile, threading, time
        from pathlib import Path as P
        from unittest import mock
        import plex_bench as pb
        calls = {"n": 0}

        def capture(pid):
            calls["n"] += 1
            return [{"pid": 9, "process_creation_time": 1,
                     "argv": ["Plex Transcoder"], "cmdline": "Plex Transcoder"}]

        with _fake_server({"seg_fail": True}) as (srv, base), \
                tempfile.TemporaryDirectory() as d, \
                mock.patch.object(win, "sample_tree_win",
                                  return_value=self._sample(100)), \
                mock.patch.object(win, "capture_transcoder_cmdlines_win",
                                  side_effect=capture):
            with self.assertRaises(pb.BenchError):
                win.run_session_win(_fake_cfg(base), 123, P(d), 0)
            alive = [t for t in threading.enumerate()
                     if t.name == "plex-bench-argv-0" and t.is_alive()]
            n0 = calls["n"]
            time.sleep(0.2)
        self.assertEqual(alive, [])
        self.assertEqual(calls["n"], n0)

    def test_native_capture_no_powershell_and_stale_pid_rejected(self):
        from unittest import mock
        snap = ({1: 0, 2: 1}, {1: "Plex Media Server.exe",
                               2: "Plex Transcoder.exe"})
        with mock.patch.object(win, "_snapshot", return_value=snap), \
                mock.patch.object(win, "_proc_creation_time", return_value=111), \
                mock.patch.object(win.subprocess, "run",
                                  side_effect=AssertionError("powershell")):
            out = win.capture_transcoder_cmdlines_win(
                1, cmdline_reader=lambda pid: "Plex Transcoder.exe -nvenc x")
        self.assertEqual(len(out), 1)
        self.assertIn("nvenc", out[0]["cmdline"])
        # Stale PID: creation stamp changes between snapshot and read.
        with mock.patch.object(win, "_snapshot", return_value=snap), \
                mock.patch("sys.platform", "win32"), \
                mock.patch.object(win, "_proc_creation_time",
                                  side_effect=[111, 222]):
            stale = win.capture_transcoder_cmdlines_win(
                1, cmdline_reader=lambda pid: "Plex Transcoder.exe -nvenc")
        self.assertEqual(stale, [])

    def test_new_win32_signatures_pointer_safe(self):
        self.assertEqual(win.WIN32_SIGNATURES["ReadProcessMemory"][0], "BOOL")
        self.assertEqual(win.WIN32_SIGNATURES["NtQueryInformationProcess"][1][0],
                         "HANDLE")


class TestProcessTree(unittest.TestCase):
    def test_tree_includes_only_descendants(self):
        ppid = {10: 1, 11: 10, 12: 11, 13: 10, 99: 1}
        self.assertEqual(sorted(win.build_process_tree(ppid, 10)), [10, 11, 12, 13])

    def test_tree_root_only(self):
        self.assertEqual(win.build_process_tree({1: 0}, 1), [1])

    def test_tree_cycle_safe(self):
        self.assertEqual(sorted(win.build_process_tree({1: 2, 2: 1}, 1)), [1, 2])


if __name__ == "__main__":
    unittest.main(verbosity=2)
