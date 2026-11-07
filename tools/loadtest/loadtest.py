#!/usr/bin/env python3
"""Load test for rbac-go GET /api/rbac/v1/access/ (the koku critical path).

Stdlib only. Each worker thread owns one keep-alive HTTPConnection so the
numbers reflect server+DB time, not TCP/TLS handshakes.

Each request picks a user at random (seeded per thread). Threads must not
walk the user list in lockstep: all threads hitting the same user at once
causes a cache stampede that a real caller mix would not produce.

Usage:
  python3 loadtest.py --base-url http://localhost:8080 --org 100001 \\
      --users test-user-0..test-user-99 --concurrency 20 --requests 2000

Users must already exist (seed them first, e.g. with
`rbac-go bootstrap --org 100001 --users $(...))`. All users share one
x-rh-identity shape; only the username varies.

Compares runs via --save/--compare JSON result files.
"""

import argparse
import base64
import concurrent.futures
import http.client
import json
import random
import statistics
import sys
import time
import urllib.parse


def identity_header(org_id, username, is_admin=False):
    payload = {
        "org_id": org_id,
        "identity": {
            "account_number": "1111111",
            "org_id": org_id,
            "type": "User",
            "user": {
                "username": username,
                "email": f"{username}@example.com",
                "is_org_admin": is_admin,
            },
        },
    }
    return base64.b64encode(json.dumps(payload).encode()).decode()


def percentile(data, pct):
    if not data:
        return 0.0
    ordered = sorted(data)
    k = (len(ordered) - 1) * (pct / 100.0)
    lo, hi = int(k), min(int(k) + 1, len(ordered) - 1)
    frac = k - lo
    return ordered[lo] * (1 - frac) + ordered[hi] * frac


def expand_users(spec):
    """Accept 'alice' or 'base-0..N' ranges."""
    import re

    m = re.fullmatch(r"(.+)-(\d+)\.\.(\d+)", spec)
    if not m:
        return [spec]
    prefix, lo, hi = m.group(1), int(m.group(2)), int(m.group(3))
    return [f"{prefix}-{i}" for i in range(lo, hi + 1)]


def worker(base, path, headers_list, picks):
    """One thread: reuse a single connection, record latencies in ms."""
    url = urllib.parse.urlparse(base)
    conn_cls = http.client.HTTPSConnection if url.scheme == "https" else http.client.HTTPConnection
    port = url.port or (443 if url.scheme == "https" else 80)
    conn = conn_cls(url.hostname, port, timeout=30)
    lat, errors = [], 0
    try:
        for pick in picks:
            headers = {"X-Rh-Identity": headers_list[pick]}
            start = time.perf_counter()
            try:
                conn.request("GET", path, headers=headers)
                resp = conn.getresponse()
                resp.read()
                if resp.status != 200:
                    errors += 1
                else:
                    lat.append((time.perf_counter() - start) * 1000.0)
            except Exception:
                errors += 1
                try:
                    conn.close()
                except Exception:
                    pass
                conn = conn_cls(url.hostname, port, timeout=30)
    finally:
        conn.close()
    return lat, errors


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--base-url", required=True)
    ap.add_argument("--api-prefix", default="/api/rbac/v1",
                    help="API path prefix (Django default is /api/v1; operator sets /api/rbac)")
    ap.add_argument("--org", required=True)
    ap.add_argument("--users", required=True, help="'alice' or 'user-0..99'")
    ap.add_argument("--application", default="cost-management")
    ap.add_argument("--limit", type=int, default=100)
    ap.add_argument("--concurrency", type=int, default=20)
    ap.add_argument("--requests", type=int, default=2000, help="total requests")
    ap.add_argument("--warmup", type=int, default=200, help="warmup requests (excluded)")
    ap.add_argument("--save", default=None, help="write JSON result to file")
    ap.add_argument("--compare", default=None, help="compare against saved JSON result")
    args = ap.parse_args()

    usernames = expand_users(args.users)
    headers = [identity_header(args.org, u) for u in usernames]
    path = f"{args.api_prefix}/access/?application={args.application}&limit={args.limit}"

    def run(total):
        per_thread = total // args.concurrency
        lat, errors = [], 0
        t0 = time.perf_counter()
        with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as ex:
            futs = []
            for t in range(args.concurrency):
                rng = random.Random(t)
                picks = [rng.randrange(len(headers)) for _ in range(per_thread)]
                futs.append(ex.submit(worker, args.base_url, path, headers, picks))
            for f in concurrent.futures.as_completed(futs):
                l, e = f.result()
                lat.extend(l)
                errors += e
        return lat, errors, time.perf_counter() - t0

    worker(args.base_url, path, headers, range(len(headers)))  # prime: one request per user
    run(args.warmup)  # warmup: connections, caches, query plans
    lat, errors, elapsed = run(args.requests)

    result = {
        "endpoint": path,
        "users": len(usernames),
        "concurrency": args.concurrency,
        "requests": len(lat),
        "errors": errors,
        "elapsed_s": round(elapsed, 3),
        "rps": round(len(lat) / elapsed, 1) if elapsed else 0,
        "mean_ms": round(statistics.mean(lat), 3) if lat else 0,
        "p50_ms": round(percentile(lat, 50), 3),
        "p95_ms": round(percentile(lat, 95), 3),
        "p99_ms": round(percentile(lat, 99), 3),
        "max_ms": round(max(lat), 3) if lat else 0,
    }
    print(json.dumps(result, indent=2))
    if args.save:
        with open(args.save, "w") as f:
            json.dump(result, f, indent=2)
        print(f"saved to {args.save}", file=sys.stderr)
    if args.compare:
        with open(args.compare) as f:
            base = json.load(f)
        print("\nvs baseline %s:" % args.compare, file=sys.stderr)
        for k in ("mean_ms", "p50_ms", "p95_ms", "p99_ms", "max_ms", "rps"):
            b, c = base.get(k, 0), result.get(k, 0)
            delta = ((c - b) / b * 100) if b else 0
            print(f"  {k}: baseline={b} current={c} ({delta:+.1f}%)", file=sys.stderr)


if __name__ == "__main__":
    main()
