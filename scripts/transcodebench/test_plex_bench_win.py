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
