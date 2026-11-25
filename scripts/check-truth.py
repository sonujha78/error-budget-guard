#!/usr/bin/env python3
"""Checks one recorded scenario against its ground truth (truth.json written by the load generator)."""
import collections, json, re, subprocess, sys

d = sys.argv[1].rstrip("/")
truth = json.load(open(f"{d}/truth.json"))
raw = open(f"{d}/access.log").read()
lines = [json.loads(l) for l in raw.splitlines() if l.strip()]
planned = truth["planned"]
ok = True


def check(name, cond, detail):
    global ok
    print(("PASS " if cond else "FAIL ") + name + ": " + detail)
    ok = ok and cond


check("every planned request reached the log", len(lines) == truth["sent"] == planned["total"],
      f"log={len(lines)} sent={truth['sent']} planned={planned['total']}")
check("no transport errors", truth["transport_errors"] == 0, str(truth["transport_errors"]))
st = collections.Counter(str(l["status"]) for l in lines)
check("status counts in the log equal what the load generator saw", dict(st) == truth["observed_status"],
      f"{dict(st)} vs {truth['observed_status']}")
errors = sum(1 for l in lines if l["status"] >= 500)
check("5xx equal the injected errors", errors == planned["errors"], f"{errors} vs {planned['errors']}")
slow = [l["latency_s"] for l in lines if l["status"] < 500 and l["latency_s"] > 0.3]
check("slow requests equal the injected slow requests (nothing unplanned was slow)", len(slow) == planned["slow"],
      f"{len(slow)} vs {planned['slow']}")
delays = [f["delay_ms"] for f in planned["faults"] if f["kind"] == "slow"]
if slow and delays:
    check("slow requests took at least the injected delay", min(slow) >= min(delays) / 1000 - 0.01, f"min {min(slow)}s")
first, last = min(l["ts"] for l in lines), max(l["ts"] for l in lines)
check("log times fall inside the run", truth["start_unix"] - 0.5 <= first and last <= truth["end_unix"] + 1.0,
      f"{first:.1f}..{last:.1f} vs {truth['start_unix']:.1f}..{truth['end_unix']:.1f}")

secrets = ["tok_DEMO_SECRET", "uid-9f31c0", "SESS999"]
ip_re = re.compile(r"\b172\.\d+\.\d+\.\d+\b")
for s in secrets:
    check(f"raw log really contains {s} (so the leak check means something)", s in raw, "yes" if s in raw else "no")
check("raw log really contains a client IP", ip_re.search(raw) is not None, "yes" if ip_re.search(raw) else "no")


def events(args):
    out = subprocess.run(["./bin/ebg", "parse"] + args, capture_output=True, text=True, check=True)
    for s in secrets:
        check(f"no {s} in `ebg parse {args[2]}` output", s not in out.stdout and s not in out.stderr, "clean")
    check(f"no IP in `ebg parse {args[2]}` output", not ip_re.search(out.stdout + out.stderr), "clean")
    tot = collections.defaultdict(lambda: [0, 0])
    for l in out.stdout.splitlines():
        e = json.loads(l)
        tot[e["sli"]][0] += e["good"]
        tot[e["sli"]][1] += e["valid"]
    return {k: tuple(v) for k, v in sorted(tot.items())}


a = events(["--slo", "configs/demo/slos.yaml", "--format", "json", f"{d}/access.log"])
b = events(["--slo", "configs/demo/slos.yaml", "--format", "prom", "--service", "demo", f"{d}/metrics.txt"])
check("nginx log and Prometheus scrapes agree on (good, valid) per SLI", a == b, f"{a} vs {b}")
sys.exit(0 if ok else 1)
