#!/usr/bin/env python3
"""Offline tests for the macOS driver: no PMS launch, no port 32400 bind.

Covers ps parsing/tree math, delta honesty, isolation symlink guard,
launch gate, port guard, and verify-all. The fake HTTP session lives in
test_plex_bench.py; here the owned-tree argv gate is exercised with a
stub root pid that has no Transcoder descendant.
"""
import http.server, json, socket, sys, tempfile, threading, unittest
from pathlib import Path
sys.path.insert(0, str(Path(__file__).parent))
import plex_bench as pb
import plex_bench_mac as pm
import test_plex_bench as fake

class TestPsParsing(unittest.TestCase):
    def test_parse_ps_time(self):
        self.assertEqual(pm.parse_ps_time("0:07.50"), 7.5)
        self.assertEqual(pm.parse_ps_time("01:02:03"), 3723.0)
        self.assertAlmostEqual(pm.parse_ps_time("1-00:00:01.0"), 86401.0)
    def test_tree_and_sample_from_rows(self):
        rows = [
            {"pid": 10, "ppid": 1, "pgid": 10, "rss_bytes": 1000, "cpu_seconds": 2.0, "args": "Plex Media Server"},
            {"pid": 11, "ppid": 10, "pgid": 10, "rss_bytes": 2000, "cpu_seconds": 3.0, "args": "Plex Transcoder -codec h264_videotoolbox"},
            {"pid": 99, "ppid": 1, "pgid": 99, "rss_bytes": 9000, "cpu_seconds": 9.0, "args": "unrelated"},
        ]
        self.assertEqual(sorted(pm.tree_pids(rows, 10)), [10, 11])
        s = pm.sample_tree(10, rows)
        self.assertEqual(s["cpu_seconds"], 5.0); self.assertEqual(s["rss_bytes"], 3000)
        self.assertIsNone(s["read_bytes"]); self.assertIsNone(s["write_bytes"])  # explicit null, not zero
        caps = pm.capture_transcoder_cmdlines(10, rows)
        self.assertEqual(len(caps), 1); self.assertIn("videotoolbox", caps[0]["cmdline"])
    def test_delta_negative_invalid_not_faked(self):
        start = {"cpu_seconds": 10.0, "read_bytes": None, "write_bytes": None, "processes": [{"pid": 1}]}
        end = {"cpu_seconds": 4.0, "read_bytes": None, "write_bytes": None, "processes": [{"pid": 1}]}
        cpu, io, valid = pm.compute_tree_delta(start, end)
        self.assertFalse(valid); self.assertIsNone(cpu); self.assertIsNone(io)
        end2 = {"cpu_seconds": 12.5, "read_bytes": None, "write_bytes": None, "processes": [{"pid": 1}]}
        cpu, io, valid = pm.compute_tree_delta(start, end2)
        self.assertTrue(valid); self.assertEqual(cpu, 2.5); self.assertIsNone(io)  # io unavailable on mac

class TestIsolation(unittest.TestCase):
    def make(self, td):
        home = Path(td) / "home"; lab = Path(td) / "lab"
        (home / "Library" / "Application Support").mkdir(parents=True)
        (home / "Library" / "Preferences").mkdir(parents=True)
        return pm.MacIsolation(lab, home=home), home
    def test_symlink_create_and_restore_absence(self):
        with tempfile.TemporaryDirectory() as td:
            iso, home = self.make(td)
            info = iso.prepare()
            self.assertTrue(info["symlink_created"])
            self.assertTrue((home / "Library" / "Application Support" / "Plex Media Server").is_symlink())
            out = iso.restore()
            self.assertFalse((home / "Library" / "Application Support" / "Plex Media Server").exists())
            self.assertEqual(out["after"]["default_support"], "absent")
    def test_refuses_preexisting_data(self):
        with tempfile.TemporaryDirectory() as td:
            iso, home = self.make(td)
            (home / "Library" / "Application Support" / "Plex Media Server").mkdir()
            with self.assertRaises(pb.BenchError):
                iso.prepare()
    def test_refuses_preexisting_prefs(self):
        with tempfile.TemporaryDirectory() as td:
            iso, home = self.make(td)
            (home / "Library" / "Preferences" / "com.plexapp.plexmediaserver.plist").write_text("x")
            with self.assertRaises(pb.BenchError):
                iso.prepare()
            # untouched
            self.assertTrue((home / "Library" / "Preferences" / "com.plexapp.plexmediaserver.plist").exists())
    def test_restore_never_removes_foreign_symlink(self):
        with tempfile.TemporaryDirectory() as td:
            iso, home = self.make(td)
            iso.prepare()
            # someone swaps our symlink for a foreign one before restore
            link = home / "Library" / "Application Support" / "Plex Media Server"
            link.unlink(); other = Path(td) / "other"; other.mkdir(); link.symlink_to(other)
            iso.restore()
            self.assertTrue(link.is_symlink())  # left alone

class TestLaunchGate(unittest.TestCase):
    def test_no_window_grant_refuses_start(self):
        cfg = pm.MacConfig(window_granted=False)
        pms = pm.PmsProcessMac(cfg, pm.MacIsolation(Path("/tmp/never-created-lab")))
        with self.assertRaises(pb.BenchError) as cm:
            pms.start()
        self.assertIn("window-granted", str(cm.exception))
    def test_port_guard(self):
        srv = socket.socket(); srv.bind(("127.0.0.1", 0)); srv.listen(1)
        port = srv.getsockname()[1]
        try:
            self.assertTrue(pm.port_connectable(f"http://127.0.0.1:{port}"))
        finally:
            srv.close()
        self.assertFalse(pm.port_connectable(f"http://127.0.0.1:{port}"))

class TestVerifyAll(unittest.TestCase):
    def test_verify_all_sessions_and_mismatch(self):
        with tempfile.TemporaryDirectory() as td:
            out = Path(td)
            cfg = pm.MacConfig(preserve_media=True)
            # stub ffprobe via a tiny executable on the fly is platform-specific;
            # instead exercise the no-segments aggregation honestly.
            v = pm.verify_all(cfg, out, [{"session_index": 0}, {"session_index": 1}])
            self.assertFalse(v["checked"]); self.assertFalse(v["matches_codec"])
            self.assertEqual(len(v["sessions"]), 2)

class TestOwnedArgvGate(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), fake.Handler)
        cls.port = cls.srv.server_address[1]
        threading.Thread(target=cls.srv.serve_forever, daemon=True).start()
    @classmethod
    def tearDownClass(cls): cls.srv.shutdown()
    def test_session_without_transcoder_descendant_rejected(self):
        fake.Handler.mode = "ok"
        cfg = pm.MacConfig(base_url=f"http://127.0.0.1:{self.port}", duration_s=10.0, timeout_s=20)
        import os
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(pb.BenchError) as cm:
                pm.run_session_mac(cfg, os.getpid(), Path(d), 0)  # our tree has no Plex Transcoder
        self.assertIn("no Plex Transcoder argv", str(cm.exception))

    def test_preflight_shape_no_launch(self):
        cfg = pm.MacConfig(fixture="/nonexistent.mp4")
        pf = pm.preflight(cfg)
        self.assertIn("isolation_preexisting", pf); self.assertIn("plan", pf)
        self.assertFalse(pf["fixture"]["exists"])

if __name__ == "__main__":
    unittest.main(verbosity=2)
