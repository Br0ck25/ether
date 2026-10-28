#!/usr/bin/env python3
"""Controlled real-client NNTP comparison. No providers or external media needed.

Requires a built cmd/usenetbench and an installed SABnzbd Python environment.
Every client gets a fresh fixture server and isolated output/config directories.
The latency shim delays packets in transit, independently of request boundaries;
its optional bandwidth budget is shared across all downstream connections.
"""
import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import queue
import signal
import socket
import subprocess
import threading
import time
import urllib.parse
import urllib.request


class Link:
    def __init__(self, latency=0, rate=0, jitter=0):
        self.latency, self.rate, self.jitter = latency, rate, jitter
        self.lock = threading.Lock()
        self.available = 0
        self.sequence = 0

    def delivery(self, size):
        with self.lock:
            now = time.monotonic()
            self.sequence += 1
            jitter = ((self.sequence * 17) % 31) / 30 * self.jitter
            due = now + self.latency + jitter
            if self.rate:
                self.available = max(now, self.available) + size / self.rate
                due = max(due, self.available)
            return due


class Proxy:
    def __init__(self, upstream, downstream, latency, chunk_bytes=65536):
        self.upstream, self.downstream, self.latency = upstream, downstream, latency
        self.chunk_bytes = chunk_bytes
        self.stop = threading.Event()
        self.listener = socket.socket()
        self.listener.bind(("127.0.0.1", 0))
        self.listener.listen()
        self.listener.settimeout(.2)
        self.port = self.listener.getsockname()[1]
        self.sockets, self.threads = [], []
        self.lock = threading.Lock()
        self.spawn(self.accept)

    def spawn(self, fn, *args):
        t = threading.Thread(target=fn, args=args)
        with self.lock:
            self.threads.append(t)
        t.start()

    def accept(self):
        while not self.stop.is_set():
            try:
                client, _ = self.listener.accept()
            except socket.timeout:
                continue
            except OSError:
                break
            try:
                server = socket.create_connection(self.upstream, 3)
                server.settimeout(None)
            except OSError:
                client.close()
                continue
            with self.lock:
                self.sockets.extend([client, server])
            self.spawn(self.pipe, client, server, Link(self.latency))
            self.spawn(self.pipe, server, client, self.downstream)

    def pipe(self, source, dest, link):
        packets = queue.Queue(16)  # bounded chunks, smaller on very slow links

        def read():
            try:
                while not self.stop.is_set():
                    data = source.recv(self.chunk_bytes)
                    item = (link.delivery(len(data)), data)
                    while not self.stop.is_set():
                        try:
                            packets.put(item, timeout=.1)
                            break
                        except queue.Full:
                            pass
                    if not data:
                        return
            except OSError:
                pass
            finally:
                while not self.stop.is_set():
                    try:
                        packets.put((time.monotonic(), b""), timeout=.1)
                        break
                    except queue.Full:
                        pass

        self.spawn(read)
        try:
            while not self.stop.is_set():
                try:
                    due, data = packets.get(timeout=.1)
                except queue.Empty:
                    continue
                if self.stop.wait(max(0, due - time.monotonic())):
                    break
                if not data:
                    break
                dest.sendall(data)
        except OSError:
            pass
        finally:
            with contextlib.suppress(OSError):
                dest.shutdown(socket.SHUT_WR)

    def close(self):
        self.stop.set()
        self.listener.close()
        with self.lock:
            for sock in self.sockets:
                with contextlib.suppress(OSError):
                    sock.shutdown(socket.SHUT_RDWR)
                sock.close()
        # Accept exits before the remaining worker list is captured.
        self.threads[0].join(4)
        for t in list(self.threads):
            t.join(4)
        if any(t.is_alive() for t in self.threads):
            raise RuntimeError("proxy did not shut down")


@contextlib.contextmanager
def child(argv, log, stdout=None):
    with open(log, "w") as output:
        p = subprocess.Popen(argv, stdout=stdout or output, stderr=output, start_new_session=True)
        try:
            yield p
        finally:
            if p.poll() is None:
                os.killpg(p.pid, signal.SIGTERM)
                try:
                    p.wait(10)
                except subprocess.TimeoutExpired:
                    os.killpg(p.pid, signal.SIGKILL)
                    p.wait()
            if p.stdout:
                p.stdout.close()


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def api(port, **kwargs):
    query = urllib.parse.urlencode(dict(apikey="local-benchmark-only", output="json", **kwargs))
    with urllib.request.urlopen(f"http://127.0.0.1:{port}/api?{query}", timeout=3) as response:
        return json.load(response)


def proc_usage(pid):
    try:
        fields = Path(f"/proc/{pid}/stat").read_text().split()
        cpu = (int(fields[13]) + int(fields[14])) / os.sysconf("SC_CLK_TCK")
        status = Path(f"/proc/{pid}/status").read_text().splitlines()
        rss = next(int(x.split()[1]) for x in status if x.startswith("VmHWM:"))
        return cpu, rss
    except (OSError, StopIteration):
        return 0, 0


def sab_download(args, folder, manifest):
    port = free_port()
    config = ["[misc]", f"host = 127.0.0.1", f"port = {port}", "api_key = local-benchmark-only",
              "auto_browser = 0", "check_new_rel = 0", "enable_https = 0", "ipv6_servers = 0",
              "enable_unrar = 0", "direct_unpack = 0", "enable_par_cleanup = 0", "fast_fail = 0",
              "safe_postproc = 0", "pre_check = 0", "folder_rename = 0", "pause_on_post_processing = 0",
              f"download_dir = {folder / 'incomplete'}", f"complete_dir = {folder / 'complete'}",
              f"admin_dir = {folder / 'admin'}", f"log_dir = {folder / 'logs'}", "[servers]"]
    for i, provider in enumerate(manifest["providers"]):
        config += [f"[[server{i}]]", f"host = {provider['host']}", f"port = {provider['port']}",
                   "ssl = 0", f"connections = {args.connections}", "enable = 1", f"priority = {i}",
                   "timeout = 20", "retention = 0"]
    cfg = folder / "sab.ini"
    cfg.write_text("\n".join(config) + "\n")
    with child([args.sab_python, str(Path(args.sab) / "SABnzbd.py"), "--config", str(cfg),
                "--browser", "0", "--logging", "1"], folder / "sab.log") as p:
        deadline = time.monotonic() + 30
        while True:
            try:
                api(port, mode="version")
                break
            except Exception:
                if time.monotonic() >= deadline or p.poll() is not None:
                    raise RuntimeError(f"SAB startup failed; see {folder / 'sab.log'}")
                time.sleep(.1)
        cpu0, _ = proc_usage(p.pid)
        start = time.monotonic()
        added = api(port, mode="addlocalfile", name=manifest["nzb"], pp=0, priority=0)
        if not added.get("status"):
            raise RuntimeError(f"SAB add failed: {added}")
        job = added["nzo_ids"][0]
        while time.monotonic() - start < args.timeout:
            history = api(port, mode="history", limit=10)["history"]["slots"]
            match = next((s for s in history if s["nzo_id"] == job), None)
            files = [f for f in (folder / "complete").rglob("*")
                     if f.is_file() and f.stat().st_size == manifest["size"]]
            if match and (match["status"] == "Failed" or (match["status"] == "Completed" and files)):
                break
            time.sleep(.05)
        else:
            raise RuntimeError("SAB download timed out")
        if len(files) == 1:
            with files[0].open("rb") as output:
                os.fsync(output.fileno())
        elapsed = time.monotonic() - start
        cpu1, rss = proc_usage(p.pid)
        if match["status"] != "Completed" or len(files) != 1:
            raise RuntimeError(f"SAB completion failed: {match}")
        with files[0].open("rb") as f:
            digest = hashlib.file_digest(f, "sha256").hexdigest()
        if digest != manifest["sha256"]:
            raise RuntimeError("SAB SHA256 mismatch")
        return dict(client="sabnzbd", fullSeconds=elapsed, sha256=digest, verified=True,
                    cpuSeconds=cpu1-cpu0, maxRSSKiBIncludingStartup=rss,
                    sabDownloadSeconds=match.get("download_time"), history=match)


def run(args, scenario, client, repeat):
    folder = Path(args.output).resolve() / f"{scenario}-{client}-{repeat}"
    folder.mkdir(parents=True, exist_ok=False)
    fault = scenario if scenario in ("truncate", "stall", "backfill") else "clean"
    argv = [args.ether, "serve", "-dir", str(folder), "-size-mib", str(args.size_mib),
            "-scenario", fault, "-connections", str(args.connections * 2), "-stall", "300ms"]
    with child(argv, folder / "fixture.log", subprocess.PIPE) as server:
        ready = json.loads(server.stdout.readline())
        manifest_path = Path(ready["manifest"])
        manifest = json.loads(manifest_path.read_text())
        proxies = []
        try:
            if scenario in ("latency", "constrained", "poor"):
                # Poor:120ms RTT,15ms jitter,1Mb/s aggregate (decimal bits).
                latency = .06 if scenario == "poor" else .015
                rate = 125000 if scenario == "poor" else 8 * 1024**2 if scenario == "constrained" else 0
                jitter = .015 if scenario == "poor" else .005 if rate else 0
                link = Link(latency, rate, jitter)
                for provider in manifest["providers"]:
                    # At1Mb/s a 1MiB per-flow queue reserves over8s of service
                    # before another flow can progress. Bound poor-link buffering
                    # to64KiB/flow rather than accidentally testing huge bufferbloat.
                    proxy = Proxy((provider["host"], provider["port"]), link, latency, 4096 if scenario == "poor" else 65536)
                    proxies.append(proxy)
                    provider["host"], provider["port"] = "127.0.0.1", proxy.port
                manifest_path.write_text(json.dumps(manifest))
            if client == "sabnzbd":
                result = sab_download(args, folder, manifest)
            else:
                command = [args.ether, "download", "-manifest", str(manifest_path), "-output", str(folder / "ether.bin"),
                           "-connections", str(args.connections), "-workers", str(args.connections * 4),
                           "-depth", "1" if client == "ether-depth1" else "4", "-timeout", f"{args.timeout}s"]
                if client == "ether-engine":
                    command += ["-engine", "-hot-mib", str(args.hot_mib), "-read-ahead-mib", str(args.read_ahead_mib), "-read-ahead-workers", str(args.read_ahead_workers)]
                measured = ["/usr/bin/time", "-v", "-o", str(folder / "resource.txt")] + command
                with child(measured, folder / "ether.log", subprocess.PIPE) as p:
                    out, _ = p.communicate(timeout=args.timeout)
                    if p.returncode:
                        raise RuntimeError(f"Ether failed: {out.decode()}; see {folder / 'ether.log'}")
                    result = json.loads(out)
                    result["client"] = client
                    result["resources"] = (folder / "resource.txt").read_text()
                if result.get("sha256") != manifest["sha256"]:
                    raise RuntimeError("Ether SHA256 mismatch")
            result.update(scenario=scenario, repeat=repeat, size=manifest["size"], connections=args.connections)
            (folder / "result.json").write_text(json.dumps(result, indent=2))
            return result
        finally:
            for proxy in proxies:
                proxy.close()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--ether", required=True)
    p.add_argument("--sab", required=True)
    p.add_argument("--sab-python", required=True)
    p.add_argument("--output", required=True)
    p.add_argument("--size-mib", type=int, default=64)
    p.add_argument("--connections", type=int, default=4)
    p.add_argument("--hot-mib", type=int, default=256)
    p.add_argument("--read-ahead-mib", type=int, default=64)
    p.add_argument("--read-ahead-workers", type=int, default=8)
    p.add_argument("--repeats", type=int, default=3)
    p.add_argument("--timeout", type=int, default=120)
    p.add_argument("--scenarios", default="clean,latency,constrained,truncate,stall,backfill")
    p.add_argument("--clients", default="ether-depth1,ether-depth4,ether-engine,sabnzbd")
    args = p.parse_args()
    scenarios = args.scenarios.split(",")
    clients = args.clients.split(",")
    if not set(scenarios) <= {"clean", "latency", "constrained", "poor", "truncate", "stall", "backfill"}:
        p.error("unknown scenario")
    if not set(clients) <= {"ether-depth1", "ether-depth4", "ether-engine", "sabnzbd"}:
        p.error("unknown client")
    if not 1 <= args.size_mib <= 1024 or not 1 <= args.connections <= 64 or args.repeats < 1 or args.timeout < 1:
        p.error("invalid size, connections, repeats, or timeout")
    if not 1 <= args.hot_mib <= 4096 or not 4 <= args.read_ahead_mib <= 4096 or not 1 <= args.read_ahead_workers <= 256:
        p.error("invalid engine cache or read-ahead setting (minimum read-ahead is 4 MiB)")
    failures = 0
    for repeat in range(args.repeats):
        clients = args.clients.split(",")
        if repeat % 2:
            clients.reverse()
        for scenario in scenarios:
            for client in clients:
                try:
                    result = run(args, scenario, client, repeat)
                except Exception as error:
                    failures += 1
                    result = dict(scenario=scenario, client=client, repeat=repeat, error=str(error))
                print(json.dumps(result), flush=True)
    raise SystemExit(1 if failures else 0)


if __name__ == "__main__":
    main()
