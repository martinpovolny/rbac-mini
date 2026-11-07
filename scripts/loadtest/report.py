#!/usr/bin/env python3
"""Summarise one load-test stage: markdown tables and a stdlib-only SVG chart.

Reads results/<stage>/<label>-<n>.json, written by measure_config in env.sh.
Each --group [STAGE/]LABEL=TITLE becomes one row and one chart group; STAGE/ reads
results/<STAGE>/ instead of the results directory given. Latencies and rps
are medians across that label's runs; max is the worst run; errors are summed.
Writes REPORT.md and report.svg into the results directory unless told otherwise.
"""

import argparse
import json
import math
import os
import re
import statistics
import sys
from xml.sax.saxutils import escape

DEFAULT_GROUPS = [
    ("django-warm", "Django + Valkey (policy cache on)"),
    ("django-nocache", "Django (policy cache off)"),
    ("go", "rbac-go (no cache)"),
]
LATENCY_SERIES = [("p50_ms", "p50", "#2b6cb0"), ("p95_ms", "p95", "#dd6b20"), ("p99_ms", "p99", "#c53030")]
THROUGHPUT_SERIES = [("rps", "req/s", "#2f855a")]

WIDTH = 960
MARGIN_LEFT = 80
MARGIN_RIGHT = 30
PLOT_HEIGHT = 220
BAR_WIDTH = 30
BAR_GAP = 6


def load_runs(results_dir, label):
    pattern = re.compile(re.escape(label) + r"-\d+\.json")
    runs = []
    for name in sorted(os.listdir(results_dir)):
        if pattern.fullmatch(name):
            with open(os.path.join(results_dir, name)) as f:
                runs.append((name, json.load(f)))
    return runs


def summarise(runs):
    data = [result for _, result in runs]
    summary = {key: statistics.median(r[key] for r in data) for key in ("rps", "mean_ms", "p50_ms", "p95_ms", "p99_ms")}
    summary["max_ms"] = max(r["max_ms"] for r in data)
    summary["errors"] = sum(r["errors"] for r in data)
    summary["runs"] = len(data)
    return summary


def markdown(rows):
    lines = [
        "| System | runs | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms (worst run) | errors |",
        "|---|---:|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for row in rows:
        s = row["summary"]
        lines.append(
            f"| {row['title']} | {s['runs']} | {s['rps']:.0f} | {s['mean_ms']:.1f} | {s['p50_ms']:.1f} "
            f"| {s['p95_ms']:.1f} | {s['p99_ms']:.1f} | {s['max_ms']:.1f} | {s['errors']} |"
        )
    lines += [
        "",
        "Per run:",
        "",
        "| System | run | rps | mean ms | p50 ms | p95 ms | p99 ms | max ms | errors | elapsed s |",
        "|---|---|---:|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for row in rows:
        for name, r in row["runs"]:
            lines.append(
                f"| {row['title']} | {name} | {r['rps']:.0f} | {r['mean_ms']:.1f} | {r['p50_ms']:.1f} "
                f"| {r['p95_ms']:.1f} | {r['p99_ms']:.1f} | {r['max_ms']:.1f} | {r['errors']} | {r['elapsed_s']:.1f} |"
            )
    return "\n".join(lines) + "\n"


def nice_ceiling(value):
    if value <= 0:
        return 1.0
    magnitude = 10 ** math.floor(math.log10(value))
    for step in (1, 2, 2.5, 5, 10):
        if step * magnitude >= value:
            return step * magnitude
    return 10 * magnitude


def fmt(value):
    return f"{value:.0f}" if value >= 100 else f"{value:.1f}"


def svg_text(x, y, content, size=12, anchor="middle", weight="normal", fill="#1a202c"):
    return (
        f'<text x="{x:.1f}" y="{y:.1f}" font-family="Helvetica, Arial, sans-serif" font-size="{size}" '
        f'font-weight="{weight}" text-anchor="{anchor}" fill="{fill}">{escape(str(content))}</text>'
    )


def svg_rect(x, y, width, height, fill):
    return f'<rect x="{x:.1f}" y="{y:.1f}" width="{width:.1f}" height="{height:.1f}" fill="{fill}"/>'


def svg_line(x1, y1, x2, y2, stroke="#e2e8f0"):
    return f'<line x1="{x1:.1f}" y1="{y1:.1f}" x2="{x2:.1f}" y2="{y2:.1f}" stroke="{stroke}" stroke-width="1"/>'


def draw_panel(parts, top, title, unit, series, rows):
    plot_top = top + 40
    plot_bottom = plot_top + PLOT_HEIGHT
    left, right = MARGIN_LEFT, WIDTH - MARGIN_RIGHT
    peak = max(row["summary"][key] for row in rows for key, _, _ in series)
    ceiling = nice_ceiling(peak * 1.1)

    parts.append(svg_text(left, top + 16, title, size=15, anchor="start", weight="bold"))
    for position, (_, label, color) in enumerate(series):
        x = right - (len(series) - position) * 64
        parts.append(svg_rect(x, top + 5, 12, 12, color))
        parts.append(svg_text(x + 17, top + 15, label, size=12, anchor="start"))

    for step in range(6):
        y = plot_bottom - PLOT_HEIGHT * step / 5
        parts.append(svg_line(left, y, right, y))
        parts.append(svg_text(left - 8, y + 4, f"{ceiling * step / 5:.0f}", size=11, anchor="end", fill="#4a5568"))
    parts.append(svg_text(left - 8, plot_top - 12, unit, size=11, anchor="end", fill="#4a5568"))

    slot = (right - left) / len(rows)
    span = len(series) * BAR_WIDTH + (len(series) - 1) * BAR_GAP
    for index, row in enumerate(rows):
        center = left + slot * (index + 0.5)
        x = center - span / 2
        for key, _, color in series:
            value = row["summary"][key]
            height = PLOT_HEIGHT * value / ceiling
            parts.append(svg_rect(x, plot_bottom - height, BAR_WIDTH, height, color))
            parts.append(svg_text(x + BAR_WIDTH / 2, plot_bottom - height - 5, fmt(value), size=10))
            x += BAR_WIDTH + BAR_GAP
        parts.append(svg_text(center, plot_bottom + 18, row["title"], size=12))
    return plot_bottom + 40


def chart(rows, subtitle):
    parts = [
        svg_text(MARGIN_LEFT, 28, "GET /api/…/v1/access/ — koku critical path", size=17, anchor="start", weight="bold"),
        svg_text(MARGIN_LEFT, 48, subtitle, size=12, anchor="start", fill="#4a5568"),
    ]
    bottom = draw_panel(parts, 60, "Latency (lower is better)", "ms", LATENCY_SERIES, rows)
    bottom = draw_panel(parts, bottom + 20, "Throughput (higher is better)", "req/s", THROUGHPUT_SERIES, rows)
    height = bottom + 20
    head = (
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{WIDTH}" height="{height:.0f}" '
        f'viewBox="0 0 {WIDTH} {height:.0f}">'
    )
    return "\n".join([head, '<rect width="100%" height="100%" fill="#ffffff"/>', *parts, "</svg>"]) + "\n"


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--results-dir", required=True)
    ap.add_argument("--group", action="append", metavar="[STAGE/]LABEL=TITLE",
                    help="repeatable; STAGE/ reads another stage's results; default: the baseline configurations")
    ap.add_argument("--subtitle", default="", help="line under the chart title (run conditions)")
    ap.add_argument("--svg", help="SVG output path (default: <results-dir>/report.svg)")
    ap.add_argument("--md", help="markdown output path (default: <results-dir>/REPORT.md)")
    args = ap.parse_args()

    specs = [tuple(g.split("=", 1)) for g in args.group] if args.group else DEFAULT_GROUPS
    rows = []
    for label, title in specs:
        directory, name = args.results_dir, label
        if "/" in label:
            stage, name = label.split("/", 1)
            directory = os.path.join(os.path.dirname(os.path.normpath(args.results_dir)), stage)
        runs = load_runs(directory, name)
        if not runs:
            print(f"warning: no runs for {label} in {directory}", file=sys.stderr)
            continue
        rows.append({"label": label, "title": title, "runs": runs, "summary": summarise(runs)})
    if not rows:
        sys.exit(f"no results found in {args.results_dir}")

    md = markdown(rows)
    svg = chart(rows, args.subtitle)
    md_path = args.md or os.path.join(args.results_dir, "REPORT.md")
    svg_path = args.svg or os.path.join(args.results_dir, "report.svg")
    with open(md_path, "w") as f:
        f.write(md)
    with open(svg_path, "w") as f:
        f.write(svg)
    print(md, end="")
    print(f"wrote {md_path} and {svg_path}", file=sys.stderr)


if __name__ == "__main__":
    main()
