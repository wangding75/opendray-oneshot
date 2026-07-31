#!/usr/bin/env python3
"""Validate the frozen OD-OS-03 One-shot contract without third-party packages."""

from __future__ import annotations

import argparse
import json
import re
import sys
from collections import deque
from pathlib import Path
from typing import Any

REQUIRED_RESOURCES = {
    "Task",
    "Delivery",
    "Run",
    "RuntimeContext",
    "StreamRecord",
    "StandardEvent",
    "Artifact",
}
REQUIRED_MACHINES = {"Task", "Delivery", "Run", "RuntimeContext"}
REQUIRED_ERRORS = {
    "oneshot.disabled",
    "oneshot.unsupported_provider",
    "oneshot.invalid_transition",
    "oneshot.context_not_found",
    "oneshot.context_owner_mismatch",
    "oneshot.resume_failed",
    "oneshot.run_conflict",
    "oneshot.cancel_failed",
    "oneshot.timeout",
    "oneshot.delivery_exhausted",
}
DOC_FILES = {
    "domain": "domain-model.md",
    "states": "state-machines.md",
    "api": "http-api.md",
    "events": "events.md",
    "errors": "errors.md",
}


class ValidationError(Exception):
    pass


def load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ValidationError(f"missing required JSON file: {path}") from exc
    except json.JSONDecodeError as exc:
        raise ValidationError(f"invalid JSON {path}: {exc}") from exc


def resolve_ref(schema_root: dict[str, Any], ref: str) -> dict[str, Any]:
    if not ref.startswith("#/"):
        raise ValidationError(f"unsupported schema reference: {ref}")
    node: Any = schema_root
    for token in ref[2:].split("/"):
        token = token.replace("~1", "/").replace("~0", "~")
        if not isinstance(node, dict) or token not in node:
            raise ValidationError(f"unresolvable schema reference: {ref}")
        node = node[token]
    if not isinstance(node, dict):
        raise ValidationError(f"schema reference is not an object: {ref}")
    return node


def is_type(value: Any, expected: str) -> bool:
    if expected == "object":
        return isinstance(value, dict)
    if expected == "array":
        return isinstance(value, list)
    if expected == "string":
        return isinstance(value, str)
    if expected == "integer":
        return isinstance(value, int) and not isinstance(value, bool)
    if expected == "boolean":
        return isinstance(value, bool)
    if expected == "null":
        return value is None
    return False


def validate_schema(value: Any, schema: dict[str, Any], root: dict[str, Any], path: str = "$") -> None:
    if "$ref" in schema:
        validate_schema(value, resolve_ref(root, schema["$ref"]), root, path)
        return
    if "const" in schema and value != schema["const"]:
        raise ValidationError(f"{path}: expected constant {schema['const']!r}")
    if "enum" in schema and value not in schema["enum"]:
        raise ValidationError(f"{path}: value {value!r} not in enum")
    expected = schema.get("type")
    if expected is not None:
        expected_types = [expected] if isinstance(expected, str) else expected
        if not any(is_type(value, item) for item in expected_types):
            raise ValidationError(f"{path}: expected type {expected_types}, got {type(value).__name__}")
    if isinstance(value, str):
        if len(value) < schema.get("minLength", 0):
            raise ValidationError(f"{path}: string shorter than minLength")
        if "pattern" in schema and re.search(schema["pattern"], value) is None:
            raise ValidationError(f"{path}: value does not match {schema['pattern']!r}")
    if isinstance(value, int) and not isinstance(value, bool):
        if "minimum" in schema and value < schema["minimum"]:
            raise ValidationError(f"{path}: integer below minimum")
        if "maximum" in schema and value > schema["maximum"]:
            raise ValidationError(f"{path}: integer above maximum")
    if isinstance(value, list):
        if len(value) < schema.get("minItems", 0):
            raise ValidationError(f"{path}: fewer than minItems")
        if "items" in schema:
            for idx, item in enumerate(value):
                validate_schema(item, schema["items"], root, f"{path}[{idx}]")
    if isinstance(value, dict):
        if len(value) < schema.get("minProperties", 0):
            raise ValidationError(f"{path}: fewer than minProperties")
        for required in schema.get("required", []):
            if required not in value:
                raise ValidationError(f"{path}: missing required property {required!r}")
        properties = schema.get("properties", {})
        additional = schema.get("additionalProperties", True)
        for key, item in value.items():
            if key in properties:
                validate_schema(item, properties[key], root, f"{path}.{key}")
            elif additional is False:
                raise ValidationError(f"{path}: unexpected property {key!r}")
            elif isinstance(additional, dict):
                validate_schema(item, additional, root, f"{path}.{key}")


def assert_unique(values: list[Any], label: str) -> None:
    seen: set[Any] = set()
    dupes: list[Any] = []
    for value in values:
        if value in seen:
            dupes.append(value)
        seen.add(value)
    if dupes:
        raise ValidationError(f"duplicate {label}: {sorted(set(dupes))}")


def validate_resources(contract: dict[str, Any]) -> None:
    resources = contract["resources"]
    if set(resources) != REQUIRED_RESOURCES:
        raise ValidationError(
            f"resource set mismatch: expected {sorted(REQUIRED_RESOURCES)}, got {sorted(resources)}"
        )
    prefixes: list[str] = []
    for name, resource in resources.items():
        prefixes.append(resource["id_prefix"])
        fields = resource["fields"]
        assert_unique([field["name"] for field in fields], f"{name} field")
        for item in fields:
            if item["type"] == "enum" and not item.get("enum"):
                raise ValidationError(f"{name}.{item['name']}: enum field has no values")
        if name == "RuntimeContext":
            forbidden = {"session_id", "sessionid", "pid", "pty", "process_handle"}
            names = {item["name"].lower().replace("_", "") for item in fields}
            if names & {x.replace("_", "") for x in forbidden}:
                raise ValidationError("RuntimeContext exposes Interactive/process identity")
    assert_unique(prefixes, "resource ID prefix")


def validate_state_machines(contract: dict[str, Any]) -> None:
    machines = contract["state_machines"]
    if set(machines) != REQUIRED_MACHINES:
        raise ValidationError(
            f"state machine set mismatch: expected {sorted(REQUIRED_MACHINES)}, got {sorted(machines)}"
        )
    for name, machine in machines.items():
        states = machine["states"]
        state_names = [item["name"] for item in states]
        assert_unique(state_names, f"{name} state")
        state_set = set(state_names)
        if machine["initial"] not in state_set:
            raise ValidationError(f"{name}: initial state is not declared")
        transitions = machine["transitions"]
        assert_unique(
            [(item["from"], item["to"], item["command"]) for item in transitions],
            f"{name} transition",
        )
        outgoing: dict[str, set[str]] = {state: set() for state in state_set}
        for transition in transitions:
            if transition["from"] not in state_set or transition["to"] not in state_set:
                raise ValidationError(f"{name}: transition references unknown state: {transition}")
            outgoing[transition["from"]].add(transition["to"])
        kinds = {item["name"]: item["kind"] for item in states}
        for state, kind in kinds.items():
            if kind == "terminal" and outgoing[state]:
                raise ValidationError(f"{name}: terminal state {state!r} has outgoing transitions")
            if kind != "terminal" and not outgoing[state]:
                raise ValidationError(f"{name}: non-terminal state {state!r} has no outgoing transition")
        reached = {machine["initial"]}
        queue = deque([machine["initial"]])
        while queue:
            current = queue.popleft()
            for nxt in outgoing[current]:
                if nxt not in reached:
                    reached.add(nxt)
                    queue.append(nxt)
        unreachable = state_set - reached
        if unreachable:
            raise ValidationError(f"{name}: unreachable state(s): {sorted(unreachable)}")
        resource_status = next(
            (field for field in contract["resources"][name]["fields"] if field["name"] == "status"),
            None,
        )
        if resource_status is None or set(resource_status.get("enum", [])) != state_set:
            raise ValidationError(f"{name}: resource status enum does not match state machine")


def validate_api(contract: dict[str, Any]) -> None:
    operations = contract["api"]
    assert_unique([item["id"] for item in operations], "API operation id")
    assert_unique(
        [(item["protocol"], item["method"], item["path"]) for item in operations],
        "API route",
    )
    for item in operations:
        path = item["path"]
        lowered = path.lower()
        if not path.startswith(contract["api_base"] + "/"):
            raise ValidationError(f"API path outside One-shot base: {path}")
        if "/sessions" in lowered or "/custom-tasks" in lowered or "mode=oneshot" in lowered:
            raise ValidationError(f"API route conflicts with existing domain: {path}")
        if not item["audit_action"].startswith("oneshot."):
            raise ValidationError(f"API action outside One-shot namespace: {item['audit_action']}")
    operation_by_id = {item["id"]: item for item in operations}
    required = set(contract["idempotency"]["required_for"])
    if not required <= set(operation_by_id):
        raise ValidationError("idempotency.required_for references unknown API operation")
    actual = {item["id"] for item in operations if item["idempotency"] == "required"}
    if required != actual:
        raise ValidationError(
            f"required idempotency mismatch: declared {sorted(required)}, operations {sorted(actual)}"
        )


def validate_events(contract: dict[str, Any]) -> None:
    events = contract["events"]
    assert_unique([item["topic"] for item in events], "event topic")
    assert_unique([item["semantics"] for item in events], "event semantics")
    for item in events:
        if not item["topic"].startswith("oneshot.") or item["topic"].startswith("session."):
            raise ValidationError(f"event outside One-shot namespace: {item['topic']}")
        if item["subject"] not in REQUIRED_RESOURCES:
            raise ValidationError(f"event subject is not a domain resource: {item['subject']}")
        assert_unique(item["payload_required"], f"{item['topic']} payload field")


def validate_errors(contract: dict[str, Any]) -> None:
    errors = contract["errors"]
    codes = [item["code"] for item in errors]
    assert_unique(codes, "error code")
    assert_unique([item["description"] for item in errors], "error semantics")
    missing = REQUIRED_ERRORS - set(codes)
    if missing:
        raise ValidationError(f"required error code(s) missing: {sorted(missing)}")
    for item in errors:
        if not item["code"].startswith("oneshot."):
            raise ValidationError(f"error outside One-shot namespace: {item['code']}")


def read_docs(contract_root: Path) -> dict[str, str]:
    docs: dict[str, str] = {}
    for key, relative in DOC_FILES.items():
        path = contract_root / relative
        if not path.is_file():
            raise ValidationError(f"missing required contract document: {path}")
        docs[key] = path.read_text(encoding="utf-8")
        if "Contract status: Frozen by `OD-OS-03`" not in docs[key]:
            raise ValidationError(f"contract document is not frozen: {path}")
    return docs


def validate_doc_coverage(contract: dict[str, Any], docs: dict[str, str]) -> None:
    for resource in REQUIRED_RESOURCES:
        if resource not in docs["domain"]:
            raise ValidationError(f"domain-model.md does not cover resource {resource}")
    for machine_name, machine in contract["state_machines"].items():
        if machine_name not in docs["states"]:
            raise ValidationError(f"state-machines.md does not cover {machine_name}")
        for state in machine["states"]:
            if f"`{state['name']}`" not in docs["states"]:
                raise ValidationError(f"state-machines.md does not cover {machine_name}.{state['name']}")
    for operation in contract["api"]:
        if f"`{operation['path']}`" not in docs["api"]:
            raise ValidationError(f"http-api.md does not cover {operation['path']}")
    for event in contract["events"]:
        if f"`{event['topic']}`" not in docs["events"]:
            raise ValidationError(f"events.md does not cover {event['topic']}")
    for error in contract["errors"]:
        if f"`{error['code']}`" not in docs["errors"]:
            raise ValidationError(f"errors.md does not cover {error['code']}")


def validate_examples(contract: dict[str, Any], examples: dict[str, Any]) -> None:
    required = {"create_task_request", "task_envelope", "event_frame", "error_response"}
    if set(examples) != required:
        raise ValidationError(f"API example set mismatch: {sorted(examples)}")
    request = examples["create_task_request"]
    for key in ("project_id", "provider_id", "prompt", "source", "attachments", "timeout_seconds"):
        if key not in request:
            raise ValidationError(f"create_task_request missing {key}")
    task = examples["task_envelope"].get("task", {})
    task_fields = {item["name"] for item in contract["resources"]["Task"]["fields"]}
    missing = {item["name"] for item in contract["resources"]["Task"]["fields"] if item["required"]} - set(task)
    if missing:
        raise ValidationError(f"task example missing required fields: {sorted(missing)}")
    if set(task) - task_fields:
        raise ValidationError(f"task example has unknown fields: {sorted(set(task) - task_fields)}")
    topics = {item["topic"] for item in contract["events"]}
    if examples["event_frame"].get("topic") not in topics:
        raise ValidationError("event example uses unknown topic")
    codes = {item["code"] for item in contract["errors"]}
    if examples["error_response"].get("error", {}).get("code") not in codes:
        raise ValidationError("error example uses unknown code")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", default=str(Path(__file__).resolve().parents[2]))
    args = parser.parse_args()
    repo = Path(args.root).resolve()
    contract_root = repo / "docs/development/oneshot/contracts"
    schema_path = contract_root / "schema/oneshot-contract.schema.json"
    contract_path = contract_root / "fixtures/oneshot-contract.json"
    examples_path = contract_root / "fixtures/api-examples.json"

    try:
        schema = load_json(schema_path)
        contract = load_json(contract_path)
        examples = load_json(examples_path)
        validate_schema(contract, schema, schema)
        validate_resources(contract)
        validate_state_machines(contract)
        validate_api(contract)
        validate_events(contract)
        validate_errors(contract)
        validate_doc_coverage(contract, read_docs(contract_root))
        validate_examples(contract, examples)
    except ValidationError as exc:
        print(f"One-shot contract validation: FAIL: {exc}", file=sys.stderr)
        return 1

    print(
        "One-shot contract validation: PASS "
        f"({len(contract['resources'])} resources, "
        f"{len(contract['state_machines'])} state machines, "
        f"{len(contract['api'])} routes, "
        f"{len(contract['events'])} events, "
        f"{len(contract['errors'])} errors)"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
