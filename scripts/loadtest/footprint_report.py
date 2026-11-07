#!/usr/bin/env python3
"""Summarise footprint samples: CPU and memory per container and per serving path.

Reads results/<stage>/footprint-<label>.csv (written by footprint.sh) and the matching
<label>-<n>.json load-test runs for throughput. Writes FOOTPRINT.md.

CPU is docker stats CPUPerc: 100 means one core. Memory is docker stats MemUsage in MiB.
The serving path is summed per sample, so its peak is the peak of the sum, not the sum of
per-container peaks.
"""

import argparse
import csv
import json
import os
import re
import statistics
import sys
from collections import defaultdict

APP_GO = "rbac-go-srv"
APP_DJANGO = "rbac-django-srv"
DB = "rbac-baseline-db"
CACHE = "rbac-baseline-redis"

CONTAINERS = [APP_GO, APP_DJANGO, DB, CACHE]
CONFIGS = [
    ("go", "rbac-go (no cache)", [APP_GO, DB]),
    ("django-nocache", "Python: Django, policy cache off", [APP_DJANGO, DB]),
    ("django-warm", "Python + Valkey: Django, policy cache on", [APP_DJANGO, DB, CACHE]),
]

UNIT_MIB = {
    "B": 1 / 1048576, "kB": 1000 / 1048576, "KiB": 1 / 1024, "MB": 1000000 / 1048576,
    "MiB": 1, "GB": 1000000000 / 1048576, "GiB": 1024, "TB": 1000000000000 / 1048576, "TiB": 1024 * 1024,
}


def mib(text):
    used = text.split("/")[0].strip()
    match = re.fullmatch(r"([\d.]+)\s*([A-Za-z]+)", used)
    if not match:
        raise ValueError(f"unparsed memory usage: {text!r}")
    return float(match.group(1)) * UNIT_MIB[match.group(2)]


def read_samples(path):
    rows = []
    with open(path) as f:
        for record in csv.DictReader(f):
            rows.append({
                "ts": int(record["timestamp"]),
                "phase": record["phase"],
                "container": record["container"],
                "cpu": float(record["cpu"].rstrip("%")),
                "mem": mib(record["mem"]),
            })
    return rows


def mean_rps(results_dir, label):
    pattern = re.compile(re.escape(label) + r"-\d+\.json")
    values = []
    for name in os.listdir(results_dir):
        if pattern.fullmatch(name):
            with open(os.path.join(results_dir, name)) as f:
                values.append(json.load(f)["rps"])
    return statistics.mean(values) if values else None


TIME_FIELDS = {
    "real": r"([\d.]+) real",
    "user": r"([\d.]+) user",
    "sys": r"([\d.]+) sys",
    "rss": r"(\d+)\s+maximum resident set size",
}


def load_generator(results_dir, label):
    pattern = re.compile(re.escape(label) + r"-\d+\.time")
    runs = []
    for name in sorted(os.listdir(results_dir)):
        if not pattern.fullmatch(name):
            continue
        with open(os.path.join(results_dir, name)) as f:
            text = f.read()
        found = {key: re.search(expr, text) for key, expr in TIME_FIELDS.items()}
        if not all(found.values()):
            raise ValueError(f"no time report in {name}")
        real = float(found["real"].group(1))
        cpu_seconds = float(found["user"].group(1)) + float(found["sys"].group(1))
        runs.append({
            "real": real,
            "cores": cpu_seconds / real,
            "rss_mib": int(found["rss"].group(1)) / 1048576,
        })
    return runs


def path_series(samples, containers):
    totals = defaultdict(lambda: {"cpu": 0.0, "mem": 0.0})
    for row in samples:
        if row["phase"] == "load" and row["container"] in containers:
            totals[row["ts"]]["cpu"] += row["cpu"]
            totals[row["ts"]]["mem"] += row["mem"]
    return [totals[ts] for ts in sorted(totals)]


def container_stats(samples, name):
    before = [r["mem"] for r in samples if r["container"] == name and r["phase"] == "idle"]
    load = [r for r in samples if r["container"] == name and r["phase"] == "load"]
    return {
        "before": before[0] if before else None,
        "peak_mem": max((r["mem"] for r in load), default=None),
        "mean_cpu": statistics.mean(r["cpu"] for r in load) if load else None,
        "peak_cpu": max((r["cpu"] for r in load), default=None),
    }


def fmt(value, digits=0):
    if value is None:
        return "–"
    return f"{value:.{digits}f}"


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--results-dir", required=True)
    args = ap.parse_args()

    loaded = []
    for label, title, path_containers in CONFIGS:
        csv_path = os.path.join(args.results_dir, f"footprint-{label}.csv")
        if not os.path.exists(csv_path):
            print(f"warning: missing {csv_path}", file=sys.stderr)
            continue
        loaded.append((label, title, path_containers, read_samples(csv_path)))
    if not loaded:
        sys.exit(f"no footprint samples in {args.results_dir}")

    lines = [
        "## Serving path under load",
        "",
        "| Configuration | containers on the path | samples | mean CPU (cores) | peak CPU (cores) | peak memory (MiB) | rps | CPU ms per request |",
        "|---|---|---:|---:|---:|---:|---:|---:|",
    ]
    for label, title, path_containers, samples in loaded:
        series = path_series(samples, path_containers)
        cpu = [s["cpu"] for s in series]
        mem = [s["mem"] for s in series]
        rps = mean_rps(args.results_dir, label)
        mean_cores = statistics.mean(cpu) / 100 if cpu else None
        ms_per_request = (mean_cores * 1000 / rps) if (mean_cores is not None and rps) else None
        lines.append(
            f"| {title} | {', '.join(path_containers)} | {len(series)} "
            f"| {fmt(mean_cores, 2)} | {fmt(max(cpu) / 100 if cpu else None, 2)} "
            f"| {fmt(max(mem) if mem else None)} | {fmt(rps)} | {fmt(ms_per_request, 2)} |"
        )

    lines += [
        "",
        "## Load generator",
        "",
        "The load tool runs on the host, outside the serving path. Its CPU time and peak memory come from `/usr/bin/time -l` per run.",
        "",
        "| Configuration | runs | wall s | mean CPU (cores) | peak memory (MiB) |",
        "|---|---:|---:|---:|---:|",
    ]
    for label, title, _, _ in loaded:
        runs = load_generator(args.results_dir, label)
        if runs:
            lines.append(
                f"| {title} | {len(runs)} | {fmt(statistics.mean(r['real'] for r in runs), 1)} "
                f"| {fmt(statistics.mean(r['cores'] for r in runs), 2)} "
                f"| {fmt(max(r['rss_mib'] for r in runs))} |"
            )
    lines += [
        "",
        "## Per container",
        "",
        "Memory before is the first sample taken after the stack was up, before any load in that configuration.",
        "",
        "| Configuration | container | memory before (MiB) | memory peak (MiB) | mean CPU (%) | peak CPU (%) |",
        "|---|---|---:|---:|---:|---:|",
    ]
    for label, title, _, samples in loaded:
        for name in CONTAINERS:
            s = container_stats(samples, name)
            lines.append(
                f"| {title} | {name} | {fmt(s['before'])} | {fmt(s['peak_mem'])} "
                f"| {fmt(s['mean_cpu'], 1)} | {fmt(s['peak_cpu'], 1)} |"
            )

    processes = os.path.join(args.results_dir, "footprint-processes.txt")
    if os.path.exists(processes):
        with open(processes) as f:
            lines += ["", "Processes at the end of the run:", "", "```", f.read().rstrip(), "```"]

    md = "\n".join(lines) + "\n"
    out = os.path.join(args.results_dir, "FOOTPRINT.md")
    with open(out, "w") as f:
        f.write(md)
    print(md, end="")
    print(f"wrote {out}", file=sys.stderr)


if __name__ == "__main__":
    main()
