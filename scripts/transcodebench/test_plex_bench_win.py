#!/usr/bin/env python3
"""Offline tests for the native Windows driver adaptation. No Plex, no Windows required."""
import sys, unittest
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
