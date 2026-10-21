#!/usr/bin/env python3
"""Offline unittest with fake HTTP server: errors / parsing / accounting. No Plex, no load."""
import http.server, json, sys, threading, unittest
from pathlib import Path
sys.path.insert(0, str(Path(__file__).parent))
import plex_bench as pb

DECISION = b'''<MediaContainer size="1" generalDecisionCode="1000" transcodeDecisionCode="1000">
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
            return self._send(200, DECISION, "text/xml")
        if "start.m3u8" in p: return self._send(200, MASTER.encode())
        if "media.m3u8" in p: return self._send(200, (MEDIA_SHORT if self.mode=="short" else MEDIA).encode())
        if "seg" in p and p.endswith(".ts"): return self._send(200, b"\x47" + b"x"*188)
        if "/stop" in p: return self._send(200, b"ok")
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
    def tearDownClass(cls): cls.srv.shutdown()
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
    def test_non200_decision_rejected(self):
        Handler.mode = "decision500"
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(pb.BenchError):
                pb.run_session(self.cfg(), None, Path(d), 0)
        Handler.mode = "ok"

if __name__ == "__main__": unittest.main(verbosity=2)
