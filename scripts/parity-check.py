#!/usr/bin/env python3
"""Live parity checks for the IP Guard control plane.

This intentionally exercises behaviour, not just status codes. It is meant to be
run against a deployed instance (or a local control process) after changing the
control plane:

    PARITY_BASE_URL=https://host \
    ADMIN_USERNAME=... ADMIN_PASSWORD=... \
    python3 scripts/parity-check.py

Set PARITY_INSECURE=1 for a self-signed HTTPS endpoint during local testing.
No credentials are printed by this script.
"""
from __future__ import annotations

import http.cookiejar
import json
import os
import ssl
import sys
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Callable

BASE = os.environ.get("PARITY_BASE_URL", "http://127.0.0.1:3000").rstrip("/")
USER = os.environ.get("ADMIN_USERNAME", "")
PASSWORD = os.environ.get("ADMIN_PASSWORD", "")
INSECURE = os.environ.get("PARITY_INSECURE") == "1"

cookie_jar = http.cookiejar.CookieJar()


def opener() -> urllib.request.OpenerDirector:
    handlers: list[Any] = [urllib.request.HTTPCookieProcessor(cookie_jar)]
    if BASE.startswith("https://") and INSECURE:
        context = ssl.create_default_context()
        context.check_hostname = False
        context.verify_mode = ssl.CERT_NONE
        handlers.append(urllib.request.HTTPSHandler(context=context))
    return urllib.request.build_opener(*handlers)


def request(path: str, method: str = "GET", body: dict[str, Any] | None = None) -> tuple[int, Any]:
    data = None
    headers = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        with opener().open(req, timeout=180) as response:
            raw = response.read()
            return response.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        try:
            payload = json.loads(raw) if raw else None
        except json.JSONDecodeError:
            payload = raw.decode(errors="replace")
        return exc.code, payload


def fail(message: str) -> None:
    raise AssertionError(message)


def check_paged(kind: str) -> Callable[[], None]:
    def run() -> None:
        status, body = request(f"/api/lists/{urllib.parse.quote(kind)}?page=1")
        if status != 200:
            fail(f"HTTP {status}: {body}")
        if not isinstance(body, dict):
            fail("expected a page object, got a bare array")
        for key in ("items", "total", "page", "pageSize"):
            if key not in body:
                fail(f"missing {key}")
        if not isinstance(body["items"], list):
            fail("items is not an array")

    return run


def check_available_filters() -> None:
    _, default = request("/api/lists/available-ips?page=1")
    if not isinstance(default, dict) or not isinstance(default.get("items"), list):
        fail("default available-ips is not a page object")
    total = default.get("total")
    if not isinstance(total, int):
        fail("total is not an integer")

    status, body = request("/api/lists/available-ips?node_dns_status=synced&page=1")
    if status != 200 or not isinstance(body, dict):
        fail(f"node_dns_status=synced failed with HTTP {status}: {body}")
    if body.get("total", 0) > total:
        fail("synced total exceeds all total")
    for item in body.get("items", []):
        if item.get("node_dns_status") != "synced":
            fail(f"{item.get('ip')} is not marked synced")

    status, body = request("/api/lists/available-ips?node_dns_status=invalid&page=1")
    if status != 422:
        fail(f"invalid DNS status should return 422, got {status}")

    status, body = request("/api/lists/available-ips?sort=probe_delay&direction=asc&page=1")
    if status != 200 or not isinstance(body, dict):
        fail(f"probe_delay sort failed with HTTP {status}: {body}")
    for item in body.get("items", [])[:3]:
        delays = [
            probe.get("delay_ms")
            for probe in item.get("probe_delays", [])
            if probe.get("online") and probe.get("fresh") and probe.get("delay_ms") is not None
        ]
        if not delays:
            fail(f"first sorted item {item.get('ip')} has no fresh probe delay")

    status, body = request("/api/available-ips?page=1")
    if status != 200 or not isinstance(body, list):
        fail("legacy /api/available-ips must stay a bare array for compatibility")


def check_scan_progress() -> None:
    status, body = request("/api/lists/scan-records?page=1")
    if status != 200 or not isinstance(body, dict):
        fail(f"scan-records failed with HTTP {status}: {body}")
    progress = body.get("network_progress")
    if not isinstance(progress, dict):
        fail("network_progress is not an object")
    for key in ("total", "completed", "pending"):
        if not isinstance(progress.get(key), int):
            fail(f"network_progress.{key} is not an integer")
    if progress["pending"] != progress["total"] - progress["completed"]:
        fail("network_progress.pending does not equal total - completed")


def check_speed_test_route() -> None:
    status, body = request("/api/available-ips/speed-test", "POST", {"ip": "not-an-ip"})
    if status != 422:
        fail(f"invalid speed-test IP should return 422, got {status}: {body}")


def check_direct_routes() -> None:
    for path in (
        "/api/cf-scan",
        "/api/cf-pool?page=1",
        "/api/latency-probes",
        "/api/scan-records?page=1",
        "/api/dns/records?page=1",
    ):
        status, body = request(path)
        if status != 200:
            fail(f"{path} returned HTTP {status}: {body}")


def main() -> int:
    if not USER or not PASSWORD:
        print("ADMIN_USERNAME and ADMIN_PASSWORD are required", file=sys.stderr)
        return 2

    status, body = request(
        "/api/auth/login", "POST", {"username": USER, "password": PASSWORD}
    )
    if status != 200:
        print(f"login failed: HTTP {status}: {body}", file=sys.stderr)
        return 2

    checks: list[tuple[str, Callable[[], None]]] = [
        ("health", lambda: None),
        ("available-ips filters/sort", check_available_filters),
        ("scan-records network progress", check_scan_progress),
        ("speed-test route", check_speed_test_route),
        ("legacy direct routes", check_direct_routes),
    ]
    checks.extend(
        (f"paged {kind}", check_paged(kind))
        for kind in (
            "nodes",
            "networks",
            "blacklist",
            "cf-targets",
            "cf-pool",
            "certificate-issues",
            "dns-records",
            "dns-candidates",
            "dns-history",
            "scan-records",
            "latency-probes",
        )
    )

    failures = 0
    for name, check in checks:
        try:
            if name == "health":
                status, body = request("/health")
                if status != 200 or not isinstance(body, dict) or body.get("status") != "ok":
                    fail(f"health returned HTTP {status}: {body}")
            else:
                check()
            print(f"PASS  {name}")
        except Exception as exc:  # noqa: BLE001 - report every check and continue
            failures += 1
            print(f"FAIL  {name}: {exc}")

    print(f"\n{len(checks) - failures}/{len(checks)} checks passed")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
