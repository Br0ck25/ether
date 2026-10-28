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

def fake_defaults(home, calls=None):
    """Simulates /usr/bin/defaults against the fake home: write creates the
    plist, delete removes it. Never touches the real CFPreferences domain."""
    import plistlib
    plist = home / "Library" / "Preferences" / f"{pm.PREFS_DOMAIN}.plist"
    def run(argv):
        if calls is not None:
            calls.append(list(argv))
        class R: returncode = 0; stderr = ""
        if "write" in argv:
            plist.parent.mkdir(parents=True, exist_ok=True)
            plist.write_bytes(plistlib.dumps({pm.RELOC_SUPPRESS_KEY: True}))
        elif "delete" in argv and plist.is_file():
            plist.unlink()
        return R()
    return run

class TestIsolation(unittest.TestCase):
    def make(self, td, calls=None):
        home = Path(td) / "home"; lab = Path(td) / "lab"
        (home / "Library" / "Application Support").mkdir(parents=True)
        (home / "Library" / "Preferences").mkdir(parents=True)
        return pm.MacIsolation(lab, home=home,
                               relocation_destinations=[home / "Applications" / "Plex Media Server.app"],
                               defaults_runner=fake_defaults(home, calls)), home
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
            with self.assertRaises(pb.BenchError):  # truthful failure, not silent 'restored'
                iso.restore()
            self.assertTrue(link.is_symlink())  # left alone
    def test_prepare_writes_suppress_key_and_restore_removes_it(self):
        with tempfile.TemporaryDirectory() as td:
            calls = []
            iso, home = self.make(td, calls)
            info = iso.prepare()
            self.assertTrue(info["reloc_suppress_attempted"]); self.assertFalse(info["reloc_suppress_proven"])
            write = [c for c in calls if "write" in c]
            self.assertEqual(len(write), 1); self.assertIn(pm.RELOC_SUPPRESS_KEY, write[0])
            self.assertIn(pm.PREFS_DOMAIN, write[0])
            self.assertTrue((home / "Library" / "Preferences" / f"{pm.PREFS_DOMAIN}.plist").exists())
            out = iso.restore()
            self.assertTrue(out["restored_absent"])
            self.assertEqual(out["after"]["prefs_plist"], "absent")
    def test_refuses_preexisting_relocation_destination(self):
        with tempfile.TemporaryDirectory() as td:
            iso, home = self.make(td)
            (home / "Applications" / "Plex Media Server.app").mkdir(parents=True)
            with self.assertRaises(pb.BenchError) as cm:
                iso.prepare()
            self.assertIn("relocation destination", str(cm.exception))
    def test_restore_raises_when_created_prefs_cannot_be_removed(self):
        with tempfile.TemporaryDirectory() as td:
            iso, home = self.make(td)
            iso.prepare()
            plist = home / "Library" / "Preferences" / f"{pm.PREFS_DOMAIN}.plist"
            plist.unlink(); plist.mkdir()  # unlink() will fail on a directory
            with self.assertRaises(pb.BenchError):
                iso.restore()

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

SECRET = "[SECRET_ASSIGNMENT]"

class FakePms:
    """Injectable PMS double: no real server, no port."""
    def __init__(self):
        self.pid = 4242; self.pgid = 4242; self.proc = None; self.stopped = False
    def start(self): return self.pid
    def wait_identity(self): return {"claimed": "0", "version": "1.43.5-fake"}
    def stop(self): self.stopped = True; return {"pgid": self.pgid, "signalled": True, "remaining_pids": []}

class _ExitedProc:
    """Leader already exited 0 (the self-relocation failure mode)."""
    returncode = 0
    def poll(self): return 0
    def wait(self, timeout=None): return 0

class TestStopReaping(unittest.TestCase):
    def make_pms(self, td, snapshot, killpg, kill=None):
        home = Path(td) / "home"; home.mkdir(parents=True, exist_ok=True)
        dest = home / "Applications" / "Plex Media Server.app"
        iso = pm.MacIsolation(Path(td) / "lab", home=home, relocation_destinations=[dest],
                              defaults_runner=fake_defaults(home))
        app = str(Path(td) / "labapp" / "Plex Media Server.app")
        pms = pm.PmsProcessMac(pm.MacConfig(app=app, lab_root=str(Path(td) / "lab")), iso)
        pms.proc = _ExitedProc(); pms.pid = 4242; pms.pgid = 4242
        pms.term_timeout_s = 0.3; pms.kill_timeout_s = 0.3; pms.poll_interval_s = 0.01
        pms.relocation_before = {str(dest): False}
        pms._snapshot = snapshot; pms._killpg = killpg
        pms._kill = kill or (lambda pid, sig: None)
        return pms, dest, app
    def row(self, pid, pgid, args):
        return {"pid": pid, "ppid": 1, "pgid": pgid, "rss_bytes": 1, "cpu_seconds": 0.0, "args": args}
    def test_parent_exited_surviving_group_still_reaped(self):
        with tempfile.TemporaryDirectory() as td:
            kills, calls = [], {"n": 0}
            def snap():
                calls["n"] += 1
                return [self.row(11, 4242, "Plex Transcoder -codec h264")] if calls["n"] <= 5 else []
            pms, _, _ = self.make_pms(td, snap, lambda pgid, sig: kills.append((pgid, sig)))
            r = pms.stop()
            self.assertIn((4242, pm.signal.SIGTERM), kills)  # sent despite leader exit 0
            self.assertTrue(r["clean"]); self.assertEqual(r["remaining_pids"], [])
    def test_surviving_group_escalates_to_sigkill(self):
        with tempfile.TemporaryDirectory() as td:
            kills = []
            def snap():
                return [] if any(s == pm.signal.SIGKILL for _, s in kills) else \
                    [self.row(11, 4242, "Plex Transcoder -codec h264")]
            pms, _, _ = self.make_pms(td, snap, lambda pgid, sig: kills.append((pgid, sig)))
            r = pms.stop()
            self.assertIn((4242, pm.signal.SIGKILL), kills); self.assertTrue(r["clean"])
    def test_audit_failure_is_not_reported_as_gone(self):
        with tempfile.TemporaryDirectory() as td:
            def snap(): raise RuntimeError("ps boom")
            pms, _, _ = self.make_pms(td, snap, lambda pgid, sig: None)
            r = pms.stop()
            self.assertFalse(r["clean"]); self.assertIn("ps boom", r["audit_error"])
            self.assertFalse(pm.stop_is_clean(r))
    def test_permission_error_is_not_swallowed(self):
        with tempfile.TemporaryDirectory() as td:
            def killpg(pgid, sig): raise PermissionError("denied")
            pms, _, _ = self.make_pms(td, lambda: [], killpg)
            r = pms.stop()
            self.assertFalse(r["clean"]); self.assertIn("permission", r["error"].lower())
    def test_escaped_relocated_relaunch_reaped_but_cleanup_not_clean(self):
        with tempfile.TemporaryDirectory() as td:
            kills = []
            pms, dest, _ = self.make_pms(td, lambda: [], lambda pgid, sig: kills.append((pgid, sig)))
            dest.mkdir(parents=True)  # copy appeared during the run (absent before)
            exe = f"{dest}/Contents/MacOS/Plex Media Server"
            pms._snapshot = lambda: [] if any(p == 777 for p, _ in kills) else [self.row(777, 777, exe)]
            r = pms.stop()
            self.assertIn((777, pm.signal.SIGTERM), kills)  # provably ours: dest was absent before
            self.assertEqual(r["escaped_remaining"], [])
            self.assertIn(str(dest), r["relocated_destinations"])
            self.assertFalse(r["clean"])  # leftover relocated copy: root must clean it
    def test_uncertain_foreign_pms_never_signalled_and_blocks_clean(self):
        with tempfile.TemporaryDirectory() as td:
            kills = []
            foreign = "/elsewhere/Plex Media Server.app/Contents/MacOS/Plex Media Server"
            pms, _, _ = self.make_pms(td, lambda: [self.row(888, 888, foreign)],
                                      lambda pgid, sig: kills.append((pgid, sig)))
            r = pms.stop()
            self.assertNotIn(888, [pgid for pgid, _ in kills])
            self.assertEqual(r["uncertain_pids"], [888]); self.assertFalse(r["clean"])
    def test_dirty_stop_blocks_prepare_hold_success(self):
        with tempfile.TemporaryDirectory() as td:
            cfg = pm.MacConfig(lab_root=str(Path(td) / "lab"), base_url="http://127.0.0.1:9",
                               profile=pm.TokenProfile())
            pms = FakePms()
            pms.stop = lambda: {"pgid": 4242, "signalled": True, "remaining_pids": [999], "clean": False}
            sigp = Path(td) / "continue.json"
            def writer():
                import time; time.sleep(0.3); sigp.write_text(json.dumps({"action": "abort"}))
            threading.Thread(target=writer, daemon=True).start()
            meta = pm.run_prepare_hold(cfg, Path(td), Path(td) / "ready.json", sigp,
                                       window_deadline_s=20.0, stop_reserve_s=2.0, pms=pms,
                                       run_bench=lambda: self.fail("must not run"))
            self.assertFalse(meta["ok"]); self.assertFalse(meta["cleanup_clean"])
            self.assertIn("cleanup NOT clean", meta["error"])

class TestPrepareHold(unittest.TestCase):
    def cfg(self, td):
        return pm.MacConfig(lab_root=str(Path(td) / "lab"), base_url="http://127.0.0.1:9",
                            duration_s=32.0, timeout_s=120.0, profile=pm.TokenProfile())
    def test_token_file_and_profile_never_serialized(self):
        with tempfile.TemporaryDirectory() as td:
            tf = Path(td) / "tok"; tf.write_text(SECRET + "\n")
            tok = pm.load_token_file(str(tf)); self.assertEqual(tok, SECRET)
            prof = pm.TokenProfile(); prof._token = tok
            self.assertEqual(prof.headers()["X-Plex-Token"], SECRET)
            self.assertNotIn("X-Plex-Token", prof.decision_params("7", "s"))  # never a param
            self.assertNotIn(SECRET, json.dumps(prof.decision_params("7", "s")))
            import dataclasses
            self.assertNotIn(SECRET, json.dumps(dataclasses.asdict(prof)))
            self.assertNotIn(SECRET, json.dumps({"auth": "token-file (content never logged)", "token_present": True}))
            with self.assertRaises(pb.BenchError):
                pm.load_token_file(str(Path(td) / "missing"))
    def test_signal_rules(self):
        with tempfile.TemporaryDirectory() as td:
            sig = Path(td) / "continue.json"
            self.assertIsNone(pm.read_signal(sig, "run1", 0.0))
            sig.write_text(json.dumps({"action": "continue", "rating_key": "9", "run_id": "other"}))
            with self.assertRaises(pb.BenchError):
                pm.read_signal(sig, "run1", 0.0)
            sig.write_text(json.dumps({"action": "bogus"}))
            with self.assertRaises(pb.BenchError):
                pm.read_signal(sig, "run1", 0.0)
            sig.write_text(json.dumps({"action": "continue", "rating_key": "9"}))
            self.assertEqual(pm.read_signal(sig, "run1", 0.0)["rating_key"], "9")
            # stale: mtime far before readiness
            import os; os.utime(sig, (1000, 1000))
            self.assertIsNone(pm.read_signal(sig, "run1", 2000.0))
    def test_session_timeout_protects_stop_reserve(self):
        self.assertEqual(pm.session_timeout_for(100.0, 120.0, 30.0), 70.0)
        self.assertEqual(pm.session_timeout_for(1000.0, 120.0, 30.0), 120.0)
        with self.assertRaises(pb.BenchError):
            pm.session_timeout_for(20.0, 120.0, 30.0)
    def _hold(self, td, signal, deadline=30.0):
        cfg = self.cfg(td); pms = FakePms()
        ready, sigp = Path(td) / "ready.json", Path(td) / "continue.json"
        def writer():
            import time; time.sleep(0.4); sigp.write_text(json.dumps(signal))
        threading.Thread(target=writer, daemon=True).start()
        calls = {}
        def bench():
            calls["rating"] = cfg.rating_key; calls["duration"] = cfg.duration_s; calls["timeout"] = cfg.timeout_s
            return {"sessions": [{"session_index": 0, "actual_media_s": 10.0}], "waves": [{"wave": 0, "wall_s": 1.0}]}
        meta = pm.run_prepare_hold(cfg, Path(td), ready, sigp, window_deadline_s=deadline,
                                   stop_reserve_s=2.0, token=SECRET, token_file="/private/root-token-0600",
                                   pms=pms, run_bench=bench)
        return meta, pms, ready, calls
    def test_hold_continue_runs_chosen_ratingkey_and_stops(self):
        with tempfile.TemporaryDirectory() as td:
            meta, pms, ready, calls = self._hold(td, {"action": "continue", "rating_key": "77", "duration_s": 10})
            self.assertTrue(meta["ok"]); self.assertTrue(pms.stopped); self.assertTrue(meta["within_deadline"])
            self.assertEqual(calls["rating"], "77"); self.assertEqual(calls["duration"], 10.0)
            self.assertLessEqual(calls["timeout"], 28.0)
            blob = ready.read_text() + json.dumps(meta)
            self.assertNotIn(SECRET, blob)  # token never in readiness or result
            r = json.loads(ready.read_text())
            self.assertEqual(r["pid"], 4242); self.assertIn("run_id", r); self.assertEqual(r["final_state"], "done")
            # readiness carries actual config/log/token paths (paths only)
            self.assertEqual(r["token_file"], "/private/root-token-0600")
            self.assertTrue(r["config"]["pms_log"].endswith("pms.log"))
            self.assertTrue(r["log_paths"]["pms_log"].endswith("pms.log"))
            self.assertIn("support_dir", r["config"]); self.assertIn("binary", r["config"])
            self.assertEqual(meta["token_file"], "/private/root-token-0600")
            self.assertFalse(hasattr(pb, "_leaked"))
    def test_hold_abort_stops_without_benchmark(self):
        with tempfile.TemporaryDirectory() as td:
            cfg = self.cfg(td); pms = FakePms()
            ready, sigp = Path(td) / "ready.json", Path(td) / "continue.json"
            def writer():
                import time; time.sleep(0.3); sigp.write_text(json.dumps({"action": "abort"}))
            threading.Thread(target=writer, daemon=True).start()
            ran = []
            meta = pm.run_prepare_hold(cfg, Path(td), ready, sigp, window_deadline_s=20.0, stop_reserve_s=2.0,
                                       pms=pms, run_bench=lambda: ran.append(1) or {"sessions": [], "waves": []})
            self.assertFalse(meta["ok"]); self.assertTrue(pms.stopped); self.assertEqual(ran, [])
            self.assertIn("abort", meta["error"])
    def test_hold_no_signal_times_out_inside_deadline_and_stops(self):
        with tempfile.TemporaryDirectory() as td:
            cfg = self.cfg(td); pms = FakePms()
            import time; t0 = time.monotonic()
            meta = pm.run_prepare_hold(cfg, Path(td), Path(td) / "ready.json", Path(td) / "continue.json",
                                       window_deadline_s=3.0, stop_reserve_s=1.0, pms=pms,
                                       run_bench=lambda: self.fail("benchmark must not run"))
            self.assertFalse(meta["ok"]); self.assertTrue(pms.stopped)
            self.assertLess(time.monotonic() - t0, 5.0); self.assertIn("no continue signal", meta["error"])
    def test_prepare_hold_launch_still_requires_window_grant(self):
        # run_prepare_hold with no injected pms builds the real PmsProcessMac,
        # whose start() refuses without --window-granted (no PMS is launched).
        with tempfile.TemporaryDirectory() as td:
            cfg = self.cfg(td); cfg.window_granted = False
            meta = pm.run_prepare_hold(cfg, Path(td), Path(td) / "ready.json", Path(td) / "continue.json",
                                       window_deadline_s=10.0, stop_reserve_s=2.0)
            self.assertFalse(meta["ok"]); self.assertIn("window-granted", meta["error"])

class RecordingHandler(fake.Handler):
    seen = []  # list of (path, token_header)
    def do_GET(self):
        type(self).seen.append((self.path, self.headers.get("X-Plex-Token")))
        super().do_GET()

class TestTokenEverywhereHeaderOnly(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), RecordingHandler)
        cls.port = cls.srv.server_address[1]
        threading.Thread(target=cls.srv.serve_forever, daemon=True).start()
    @classmethod
    def tearDownClass(cls): cls.srv.shutdown()
    def test_every_request_header_no_param_no_result_leak(self):
        fake.Handler.mode = "ok"; RecordingHandler.mode = "ok"; RecordingHandler.seen = []
        prof = pm.TokenProfile(); prof._token = SECRET
        cfg = pm.MacConfig(base_url=f"http://127.0.0.1:{self.port}", duration_s=10.0,
                           timeout_s=20, profile=prof)
        with tempfile.TemporaryDirectory() as d:
            r = pm.run_session_mac(cfg, None, Path(d), 0)
        self.assertGreaterEqual(len(RecordingHandler.seen), 5)  # decision,start,media,segs,stop
        for path, tok in RecordingHandler.seen:
            self.assertEqual(tok, SECRET, f"missing token header on {path}")
            self.assertNotIn(SECRET, path); self.assertNotIn("X-Plex-Token", path)
        self.assertNotIn(SECRET, json.dumps(r))  # sanitized result excludes credential
        self.assertNotIn(SECRET, json.dumps(r.get("profile") or {}))
        # wrapper restored after the session
        self.assertFalse(getattr(pb.http_get, "_is_auth_wrapper", False))

if __name__ == "__main__":
    unittest.main(verbosity=2)
