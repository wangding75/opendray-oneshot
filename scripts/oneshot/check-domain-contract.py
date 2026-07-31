#!/usr/bin/env python3
"""Verify the OD-OS-07 Go domain model matches the frozen OD-OS-03 contract."""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
DOMAIN = ROOT / "internal" / "oneshot" / "domain"
CONTRACT = ROOT / "docs" / "development" / "oneshot" / "contracts" / "fixtures" / "oneshot-contract.json"

RESOURCE_FILES = {
    "Task": "task.go",
    "Delivery": "delivery.go",
    "Run": "run.go",
    "RuntimeContext": "runtime_context.go",
    "StreamRecord": "stream_record.go",
    "StandardEvent": "standard_event.go",
    "Artifact": "artifact.go",
}
STATUS_TYPES = {
    "Task": "TaskStatus",
    "Delivery": "DeliveryStatus",
    "Run": "RunStatus",
    "RuntimeContext": "RuntimeContextStatus",
}
PREFIX_CONSTANTS = {
    "Task": "taskIDPrefix",
    "Delivery": "deliveryIDPrefix",
    "Run": "runIDPrefix",
    "RuntimeContext": "runtimeContextIDPrefix",
    "StreamRecord": "streamRecordIDPrefix",
    "StandardEvent": "standardEventIDPrefix",
    "Artifact": "artifactIDPrefix",
}


class Failure(Exception):
    pass


def read(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except FileNotFoundError as exc:
        raise Failure(f"missing required file: {path.relative_to(ROOT)}") from exc


def struct_block(text: str, name: str) -> str:
    match = re.search(rf"type\s+{re.escape(name)}\s+struct\s*\{{(?P<body>.*?)\n\}}", text, re.S)
    if not match:
        raise Failure(f"missing struct {name}")
    return match.group("body")


def snapshot_fields(text: str, resource: str) -> set[str]:
    body = struct_block(text, resource + "Snapshot")
    return set(re.findall(r'json:"([^",]+)', body))


def status_values(text: str, status_type: str) -> set[str]:
    return set(re.findall(rf"\b[A-Za-z0-9_]+\s+{re.escape(status_type)}\s*=\s*\"([^\"]+)\"", text))


def check() -> None:
    contract = json.loads(read(CONTRACT))
    resources = contract["resources"]
    if set(resources) != set(RESOURCE_FILES):
        raise Failure("resource set does not match OD-OS-07 implementation map")

    id_text = read(DOMAIN / "id.go")
    for resource, filename in RESOURCE_FILES.items():
        text = read(DOMAIN / filename)
        expected_fields = {field["name"] for field in resources[resource]["fields"]}
        actual_fields = snapshot_fields(text, resource)
        if actual_fields != expected_fields:
            missing = sorted(expected_fields - actual_fields)
            extra = sorted(actual_fields - expected_fields)
            raise Failure(f"{resource}Snapshot fields mismatch: missing={missing} extra={extra}")

        prefix_name = PREFIX_CONSTANTS[resource]
        prefix_match = re.search(rf"\b{re.escape(prefix_name)}\s*=\s*\"([^\"]+)\"", id_text)
        if not prefix_match:
            raise Failure(f"missing ID prefix constant {prefix_name}")
        if prefix_match.group(1) != resources[resource]["id_prefix"]:
            raise Failure(
                f"{resource} ID prefix mismatch: {prefix_match.group(1)!r} != {resources[resource]['id_prefix']!r}"
            )

    for aggregate, status_type in STATUS_TYPES.items():
        text = read(DOMAIN / RESOURCE_FILES[aggregate])
        expected = {state["name"] for state in contract["state_machines"][aggregate]["states"]}
        actual = status_values(text, status_type)
        if actual != expected:
            raise Failure(f"{aggregate} status set mismatch: expected={sorted(expected)} actual={sorted(actual)}")

    errors_text = read(DOMAIN / "errors.go")
    expected_errors = {item["code"] for item in contract["errors"]}
    actual_errors = set(re.findall(r'\bError[A-Za-z0-9_]+\s+ErrorCode\s*=\s*"([^"]+)"', errors_text))
    if actual_errors != expected_errors:
        raise Failure(
            f"error code set mismatch: missing={sorted(expected_errors - actual_errors)} extra={sorted(actual_errors - expected_errors)}"
        )

    retryability = {
        item["code"]: item["retryable"]
        for item in contract["errors"]
    }
    for code, expected in retryability.items():
        pattern = rf"\bError[A-Za-z0-9_]+:\s*{str(expected).lower()}"
        if not re.search(pattern, errors_text):
            raise Failure(f"retryability mapping missing for {code}: expected {expected}")

    for aggregate in ("Task", "Delivery", "Run", "RuntimeContext"):
        text = read(DOMAIN / RESOURCE_FILES[aggregate])
        body = struct_block(text, aggregate)
        if re.search(r"\bStatus\s+", body):
            raise Failure(f"{aggregate} exposes mutable Status field")
        if not re.search(r"\bstatus\s+", body):
            raise Failure(f"{aggregate} does not keep status private")

    all_go = "\n".join(read(path) for path in sorted(DOMAIN.glob("*.go")))
    forbidden = [
        r'github\.com/opendray/opendray-v2/internal/session',
        r'github\.com/opendray/opendray-v2/internal/channel',
        r'github\.com/creack/pty',
        r'\bSessionID\b',
        r'\bpty\.Start\b',
        r'net/http',
        r'jackc/pgx',
    ]
    for pattern in forbidden:
        if re.search(pattern, all_go):
            raise Failure(f"forbidden domain dependency/reference matched: {pattern}")

    print("PASS: 7 resource snapshots match frozen fields and ID prefixes")
    print("PASS: 4 state sets match frozen state machines")
    print("PASS: 26 stable error codes and retryability mappings match")
    print("PASS: aggregate status fields remain private")
    print("PASS: domain has no Session, Channel, PTY, HTTP, or database dependency")


if __name__ == "__main__":
    try:
        check()
    except (Failure, json.JSONDecodeError) as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        raise SystemExit(1)
