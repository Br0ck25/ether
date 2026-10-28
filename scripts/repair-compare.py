#!/usr/bin/env python3
"""Compare cold full downloads of identical saved NZBs, Ether vs SABnzbd.

Uses isolated configuration/output and the existing Ether owner configuration.
Never connects to a user's running SABnzbd. Both clients persist output; repair
is enabled, extraction disabled. All owned children are reaped on every exit.
"""
import argparse
import contextlib
from collections import Counter
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import time
import urllib.parse
import urllib.request


def api(port, **params):
    params.update(apikey="local-repair-benchmark-only", output="json")
    with urllib.request.urlopen(f"http://127.0.0.1:{port}/api?" + urllib.parse.urlencode(params), timeout=5) as r:
        return json.load(r)


@contextlib.contextmanager
def owned(command, log):
    with open(log, "w") as output:
        p = subprocess.Popen(command, stdout=output, stderr=output, start_new_session=True)
        try:
            yield p
        finally:
            with contextlib.suppress(ProcessLookupError):
                os.killpg(p.pid, signal.SIGTERM)
            if p.poll() is None:
                try:
                    p.wait(10)
                except subprocess.TimeoutExpired:
                    os.killpg(p.pid, signal.SIGKILL)
                    p.wait()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ether", required=True)
    parser.add_argument("--sab", required=True)
    parser.add_argument("--dir", required=True)
    parser.add_argument("--sample", type=int, required=True)
    parser.add_argument("--timeout", type=int, default=900)
    parser.add_argument("--order", choices=["ether-first", "sab-first"], default="ether-first")
    parser.add_argument("--cpu-profile", help="optional Ether CPU profile output")
    args = parser.parse_args()
    folder = Path(args.dir).resolve()
    base = [args.ether, "-dir", str(folder), "-sample", str(args.sample), "-timeout", f"{args.timeout}s"]
    with open(args.ether, "rb") as binary:
        results = {"etherBinarySHA256": hashlib.file_digest(binary, "sha256").hexdigest(), "order": args.order}

    def ether():
        profile = ["-cpu-profile", args.cpu_profile] if args.cpu_profile else []
        with owned(base + ["-mode", "download", "-max-bytes", str(8 << 30)] + profile, folder / f"ether-{args.sample:03d}.log") as p:
            p.wait(args.timeout + 15)
            if p.returncode:
                raise RuntimeError("Ether download failed; inspect private log")
        data = json.loads((folder / f"ether-download-{args.sample:03d}.json").read_text())
        print(f"Ether: {data['seconds']:.3f}s failure={data['failure']!r}", flush=True)
        return data

    def sab():
        info = json.loads(subprocess.check_output(base + ["-mode", "sab-config"]))
        root = Path(info["directory"])
        with owned([args.sab, "--new", "--console", "--config-file", str(root / "sab.ini"), "--browser", "0"], root / "process.log") as p:
            start = time.monotonic()
            while True:
                try:
                    version = api(info["port"], mode="version")
                    break
                except Exception:
                    if p.poll() is not None or time.monotonic() - start > 60:
                        raise RuntimeError("SAB startup failed; inspect private log")
                    time.sleep(.2)
            start = time.monotonic()
            # +Repair, no extraction. The original saved NZB is submitted as-is.
            add = api(info["port"], mode="addlocalfile", name=info["nzb"], pp=1, priority=0)
            if not add.get("status"):
                raise RuntimeError("SAB did not accept NZB")
            job = add["nzo_ids"][0]
            last_report = start
            history = None
            while time.monotonic() - start < args.timeout:
                slots = api(info["port"], mode="history", limit=50)["history"]["slots"]
                history = next((s for s in slots if s["nzo_id"] == job), None)
                if history and history["status"] in ("Completed", "Failed"):
                    break
                if time.monotonic() - last_report > 30:
                    print(f"SAB sample {args.sample}: running {time.monotonic()-start:.0f}s", flush=True)
                    last_report = time.monotonic()
                time.sleep(.5)
            elapsed = time.monotonic() - start
            for path in (root / "complete").rglob("*"):
                if path.is_file():
                    with path.open("rb") as f:
                        os.fsync(f.fileno())
            elapsed = time.monotonic() - start
            result = dict(client="sabnzbd", version=version, sample=args.sample, seconds=elapsed,
                          status=history["status"] if history and history["status"] in ("Completed", "Failed") else "deadline", history=history,
                          cache="cold", connectionsPerProvider=4, files=[], outputDirectory=str(root))
            for path in (root / "complete").rglob("*"):
                if path.is_file() and path.suffix.lower() != ".par2":
                    with path.open("rb") as f:
                        digest = hashlib.file_digest(f, "sha256").hexdigest()
                    result["files"].append(dict(name=path.name, bytes=path.stat().st_size, sha256=digest))
            with contextlib.suppress(Exception):
                api(info["port"], mode="shutdown")
            with contextlib.suppress(subprocess.TimeoutExpired):
                p.wait(10)
            (root / "result.json").write_text(json.dumps(result, indent=2))
            print(f"SAB: {elapsed:.3f}s status={result['status']}", flush=True)
            return result

    for name in (["ether", "sabnzbd"] if args.order == "ether-first" else ["sabnzbd", "ether"]):
        results[name] = ether() if name == "ether" else sab()
    reference = Counter((f["bytes"], f["sha256"]) for f in (results["ether"]["files"] or []))
    observed = Counter((f["bytes"], f["sha256"]) for f in (results["sabnzbd"]["files"] or []))
    results["allEtherFilesMatchSAB"] = not results["ether"]["failure"] and results["sabnzbd"]["status"] == "Completed" and bool(reference) and reference <= observed
    nzb = (folder / f"sample-{args.sample:03d}.nzb").read_bytes()
    results["nzbSHA256"] = hashlib.sha256(nzb).hexdigest()
    (folder / f"comparison-{args.sample:03d}.json").write_text(json.dumps(results, indent=2))
    print("All Ether output files match SAB:", results["allEtherFilesMatchSAB"], flush=True)


if __name__ == "__main__":
    main()
