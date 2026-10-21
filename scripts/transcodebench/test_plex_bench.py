#!/usr/bin/env python3
"""Offline unittest with fake HTTP server: errors / parsing / accounting. No Plex, no load."""
import http.server, json, sys, threading, unittest
from pathlib import Path
sys.path.insert(0, str(Path(__file__).parent))
import plex_bench as pb

DECISION = b'''<MediaContainer size="1" generalDecisionCode="1001" transcodeDecisionCode="1001">
<Video ratingKey="1"><Media id="1" videoCodec="h264" audioCodec="aac" videoResolution="640x360" width="640" height="360"/></Video>
</MediaContainer>'''
MASTER = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=2000000\nmedia.m3u8\n"
MEDIA = "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\nseg0.ts\n#EXTINF:4.0,\nseg1.ts\n#EXTINF:2.0,\nseg2.ts\n#EXT-X-ENDLIST\n"
MEDIA_SHORT = "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:3.0,\nseg0.ts\n#EXT-X-ENDLIST\n"

class Handler(http.server.BaseHTTPRequestHandler):
    mode = "ok"
    def log_message(self, *a): pass
    def do_GET(self):
        p = self.path
        if p.startswith("/identity"):
            return self._send(200, b'<MediaContainer claimed="0" version="1.43.5-fake"/>', "text/xml")
        if "/decision" in p:
            if self.mode == "decision500": return self._send(500, b"boom")
            if self.mode == "decision_fail": return self._send(200, b'<MediaContainer generalDecisionCode="2000" generalDecisionText="Neither" transcodeDecisionCode="4005" transcodeDecisionText="No conversion profile found for protocol hls"/>', "text/xml")
            # negotiation must carry a real transcode target, not Generic
            assert "add-transcode-target" in p or "add-transcode-target" in (self.headers.get("X-Plex-Client-Profile-Extra") or ""), "missing profile extra"
            return self._send(200, DECISION, "text/xml")
        if "start.m3u8" in p:
            if self.mode == "start500": return self._send(500,b"start failed")
            return self._send(200, MASTER.encode())
        if "media.m3u8" in p:
            media=MEDIA_SHORT if self.mode=="short" else MEDIA
            if self.mode == "unfinished": media=media.replace("#EXT-X-ENDLIST\n", "")
            return self._send(200, media.encode())
        if "seg" in p and p.endswith(".ts"):
            idx = int(p.split("seg")[1].split(".")[0])
            if self.mode == "order" and idx == 0:
                import time; time.sleep(0.4)
            return self._send(200, bytes([65+idx]) * 189)
        if "/stop" in p:
            if self.mode == "stop404": return self._send(404, b"no session")
            return self._send(200, b"ok")
        return self._send(404, b"nope")
    def _send(self, code, body, ct="application/vnd.apple.mpegurl"):
        self.send_response(code); self.send_header("Content-Type", ct)
        self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)

class TestParsing(unittest.TestCase):
    def test_extinf_accounting_actual_not_nominal(self):
        _,_,segs,total,end = pb.parse_playlist(MEDIA, "http://x/media.m3u8")
        self.assertEqual(len(segs), 3); self.assertEqual(total, 10.0); self.assertTrue(end)
    def test_master_detection(self):
        is_m, url, *_ = pb.parse_playlist(MASTER, "http://x/start.m3u8")
        self.assertTrue(is_m); self.assertTrue(url.endswith("media.m3u8"))
    def test_unknown_duration_rejected(self):
        with self.assertRaises(pb.BenchError):
            pb.parse_playlist("#EXTM3U\nseg0.ts\n", "http://x/m.m3u8")
    def test_sanitize_redacts_token(self):
        out = pb.sanitize("GET /x?X-Plex-Token=abc123&y=1 Authorization: Bearer zzz")
        self.assertNotIn("abc123", out); self.assertNotIn("zzz", out); self.assertIn("<redacted>", out)

class TestFakeServer(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        cls.port = cls.srv.server_address[1]
        threading.Thread(target=cls.srv.serve_forever, daemon=True).start()
    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()
        cls.srv.server_close()
    def test_sessions_use_independent_client_identities(self):
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            cfg=self.cfg()
            a=pb.run_session(cfg,None,Path(d),0)
            b=pb.run_session(cfg,None,Path(d),1)
            self.assertNotEqual(a["profile"]["client_identifier"],b["profile"]["client_identifier"])
            self.assertEqual(cfg.profile.client_identifier,"ether-lab-bench")
    def test_duration_boundary_does_not_wait_for_endlist(self):
        import tempfile
        Handler.mode="unfinished"
        try:
            with tempfile.TemporaryDirectory() as d:
                result=pb.run_session(self.cfg(4),None,Path(d),0)
            self.assertEqual(result["actual_media_s"],4)
            self.assertEqual(result["segment_count"],1)
            self.assertFalse(result["playlist_endlist"])
        finally: Handler.mode="ok"
    def test_capture_cleanup_does_not_inflate_encode_time(self):
        import tempfile,time
        from unittest import mock
        Handler.mode="ok"
        def slow_capture(pid):
            time.sleep(.3)
            return [{"pid":pid,"cmdline":"Plex Transcoder -codec libx264","argv":[]}]
        with tempfile.TemporaryDirectory() as d, mock.patch.object(pb,"capture_transcoder_cmdlines",side_effect=slow_capture), mock.patch.object(pb,"sample_tree",return_value={"cpu_seconds":0,"rss_bytes":1,"read_bytes":0,"write_bytes":0,"processes":[{"pid":1}]}):
            start=time.monotonic()
            result=pb.run_session(self.cfg(4),1,Path(d),0)
            elapsed=time.monotonic()-start
        self.assertGreaterEqual(elapsed,.3)
        self.assertLess(result["encode_s"],elapsed-.15)
        self.assertLessEqual(result["measurement_finished_monotonic_s"],time.monotonic())
    def test_start_failure_stops_argv_poller(self):
        import tempfile
        from unittest import mock
        seen=[]
        def capture(pid):
            seen.append(threading.current_thread())
            return []
        Handler.mode="start500"
        try:
            with tempfile.TemporaryDirectory() as d, mock.patch.object(pb,"capture_transcoder_cmdlines",side_effect=capture), mock.patch.object(pb,"sample_tree",return_value={}):
                with self.assertRaises(pb.BenchError): pb.run_session(self.cfg(),1,Path(d),0)
            self.assertTrue(seen)
            self.assertTrue(all(not t.is_alive() for t in seen))
        finally: Handler.mode="ok"
    def cfg(self, dur=10.0):
        return pb.Config(lab_root="/nonexistent", base_url=f"http://127.0.0.1:{self.port}",
                         duration_s=dur, timeout_s=20, launch=False)
    def test_session_counts_actual_extinf(self):
        Handler.mode = "ok"
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            r = pb.run_session(self.cfg(10.0), None, Path(d), 0)
        self.assertEqual(r["actual_media_s"], 10.0)  # 4+4+2 from EXTINF, not server claim
        self.assertEqual(r["segment_count"], 3)
        self.assertGreater(r["output_bytes"], 0)
        self.assertEqual(r["requested_vs_actual"]["actual_video_codec"], "h264")
        self.assertTrue(r["stopped_session"])
    def test_short_media_does_not_fabricate_duration(self):
        Handler.mode = "short"
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            r = pb.run_session(self.cfg(10.0), None, Path(d), 0)
        self.assertEqual(r["actual_media_s"], 3.0)  # honors actual, never pads to requested 10
        Handler.mode = "ok"
    def test_decision_4005_rejected_before_start(self):
        Handler.mode = "decision_fail"
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(pb.BenchError) as cm:
                pb.run_session(self.cfg(), None, Path(d), 0)
        self.assertIn("4005", str(cm.exception))
        Handler.mode = "ok"
    def test_profile_has_real_transcode_target(self):
        p = pb.Profile()
        self.assertNotEqual(p.platform, "Generic")
        extra = p.client_profile_extra()
        self.assertIn("add-transcode-target", extra); self.assertIn("protocol=hls", extra)
        self.assertIn("videoCodec=h264", extra)
    def test_latency_is_first_body_and_files_in_playlist_order(self):
        Handler.mode = "order"
        import tempfile
        cfg = self.cfg(10.0); cfg.preserve_media = True
        with tempfile.TemporaryDirectory() as d:
            r = pb.run_session(cfg, None, Path(d), 0)
            files = sorted((Path(d)/"session-0").glob("seg-*.ts"))
            bodies = [f.read_bytes()[:1] for f in files]
        self.assertEqual(bodies, [b"A", b"B", b"C"])  # playlist order despite seg0 finishing last
        self.assertGreaterEqual(r["first_segment_latency_s"], 0.35)  # body-complete, not playlist arrival
        self.assertIsNone(r["peak_rss_bytes"]); self.assertEqual(r["cpu_sampling"], "unavailable")
        self.assertIsNone(r["tree_cpu_delta_s"])
        Handler.mode = "ok"
    def test_missing_transcoder_argv_rejected_when_owned(self):
        Handler.mode = "ok"
        import os, tempfile
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(pb.BenchError) as cm:
                pb.run_session(self.cfg(), os.getpid(), Path(d), 0)  # owned tree has no Transcoder
        self.assertIn("no Plex Transcoder argv", str(cm.exception))
    def test_failed_stop_reflected_not_true(self):
        Handler.mode = "stop404"
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            r = pb.run_session(self.cfg(), None, Path(d), 0)
        self.assertFalse(r["stopped_session"])
        Handler.mode = "ok"
    def test_negative_tree_delta_invalid(self):
        s = {"cpu_seconds": 10.0, "read_bytes": 100, "write_bytes": 100, "processes": [{"pid": 1}]}
        e = {"cpu_seconds": 5.0, "read_bytes": 50, "write_bytes": 50, "processes": [{"pid": 1}]}
        cpu, io, valid = pb.compute_tree_delta(s, e)
        self.assertFalse(valid); self.assertIsNone(cpu)
        cpu, io, valid = pb.compute_tree_delta(s, {"cpu_seconds": 12.0, "read_bytes": 150, "write_bytes": 150, "processes": [{"pid": 1}]})
        self.assertTrue(valid); self.assertEqual(cpu, 2.0)
    def test_verify_checks_all_sessions_and_fails_mismatch(self):
        import tempfile, os
        with tempfile.TemporaryDirectory() as d:
            fake = Path(d)/"fake-ffprobe"; 
            fake.write_text('#!/usr/bin/env python3\nimport json,sys\np=sys.argv[-1]\ncodec="hevc" if "session-1" in p else "h264"\nprint(json.dumps({"streams":[{"codec_name":codec,"width":640,"height":360}]}))\n')
            fake.chmod(0o755)
            for i in range(2):
                sd = Path(d)/f"session-{i}"; sd.mkdir(); (sd/"seg-00000.ts").write_bytes(b"x"*100)
            cfg = self.cfg(); cfg.preserve_media = True; cfg.ffprobe = str(fake)
            v = pb.verify_output(cfg, Path(d), [{"session_index": 0}, {"session_index": 1}])
        self.assertTrue(v["checked"]); self.assertFalse(v["matches_codec"])  # 2nd session hevc must fail aggregate
        self.assertEqual(len(v["sessions"]), 2)
    def test_main_wave_wall_and_verify_mismatch_fails(self):
        Handler.mode = "ok"
        import tempfile, json
        with tempfile.TemporaryDirectory() as d:
            fake = Path(d)/"fake-ffprobe"
            fake.write_text('#!/usr/bin/env python3\nimport json\nprint(json.dumps({"streams":[{"codec_name":"hevc","width":640,"height":360}]}))\n')
            fake.chmod(0o755)
            rc = pb.main(["--no-launch", "--base-url", f"http://127.0.0.1:{self.port}", "--lab-root", d,
                          "--duration", "4", "--concurrency", "2", "--repeats", "1", "--timeout", "20",
                          "--preserve-media", "--ffprobe", str(fake), "--out", str(Path(d)/"out")])
            meta = json.loads((Path(d)/"out"/"result.json").read_text())
        self.assertEqual(rc, 1); self.assertFalse(meta["ok"])  # mismatch must not be ok=True
        self.assertEqual(len(meta["waves"]), 1); self.assertGreater(meta["waves"][0]["wall_s"], 0)
        self.assertEqual(meta["waves"][0]["actual_media_total_s"], 8.0)  # sum of 2x4s, not per-session time
        self.assertAlmostEqual(meta["waves"][0]["aggregate_media_per_wall_s"],
                                 meta["waves"][0]["actual_media_total_s"]/meta["waves"][0]["wall_s"], places=2)
    def test_main_records_explicit_fixture_hash(self):
        Handler.mode = "ok"
        import tempfile, json, hashlib
        with tempfile.TemporaryDirectory() as d:
            fixture = Path(d)/"actual-fixture.mp4"; fixture.write_bytes(b"natural-fixture-bytes")
            (Path(d)/"media").mkdir(); (Path(d)/"media"/"source.mkv").write_bytes(b"different-default")
            rc = pb.main(["--no-launch", "--base-url", f"http://127.0.0.1:{self.port}", "--lab-root", d,
                          "--duration", "4", "--concurrency", "1", "--repeats", "1", "--timeout", "20",
                          "--fixture", str(fixture), "--out", str(Path(d)/"out")])
            meta = json.loads((Path(d)/"out"/"result.json").read_text())
        self.assertEqual(rc, 0)
        self.assertEqual(meta["source"]["fixture"], str(fixture))
        self.assertEqual(meta["source"]["sha256"], hashlib.sha256(b"natural-fixture-bytes").hexdigest())
    def test_non200_decision_rejected(self):
        Handler.mode = "decision500"
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(pb.BenchError):
                pb.run_session(self.cfg(), None, Path(d), 0)
        Handler.mode = "ok"

if __name__ == "__main__": unittest.main(verbosity=2)

class HardwareProofTests(unittest.TestCase):
    def test_decoder_only_is_not_encoder_proof(self):
        self.assertFalse(pb.hardware_encode_verified([{"cmdline": "Plex Transcoder -hwaccel nvdec -codec:0 libx264"}]))
    def test_mixed_fallback_is_not_hardware_run(self):
        self.assertFalse(pb.hardware_encode_verified([{"cmdline": "-codec:0 h264_nvenc"}, {"cmdline": "-codec:0 libx264"}]))
    def test_hardware_encoder(self):
        self.assertTrue(pb.hardware_encode_verified([{"cmdline": "-codec:0 h264_videotoolbox"}]))
