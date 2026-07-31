#!/usr/bin/env python3
from __future__ import annotations

import argparse
import pathlib
import sys

import yaml

LEGACY_KEYS = {
    "completed_tasks",
    "implementation_complete_tasks",
    "local_runtime_validation_pending",
}
IMPLEMENTATION = {"pending", "completed"}
GATE = {"pending", "passed", "failed", "not_required"}
OVERALL = {"pending", "completed", "pending_runtime_validation", "needs_changes", "blocked"}


def fail(message: str) -> None:
    raise ValueError(message)


def validate(path: pathlib.Path) -> None:
    data = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        fail("task-state root must be a mapping")

    legacy = sorted(LEGACY_KEYS.intersection(data))
    if legacy:
        fail(f"legacy duplicate status keys are forbidden: {', '.join(legacy)}")

    tasks = data.get("tasks")
    if not isinstance(tasks, dict) or not tasks:
        fail("tasks must be a non-empty mapping")

    current = data.get("current_task")
    if current not in tasks:
        fail(f"current_task {current!r} is not present in tasks")

    for required in [f"OD-OS-{i:02d}" for i in range(7)]:
        if required not in tasks:
            fail(f"missing task state: {required}")

    for task_id, state in tasks.items():
        if not isinstance(state, dict):
            fail(f"{task_id} state must be a mapping")
        implementation = state.get("implementation")
        source_gate = state.get("source_gate")
        runtime_gate = state.get("runtime_gate")
        overall = state.get("overall")
        if implementation not in IMPLEMENTATION:
            fail(f"{task_id}.implementation has invalid value {implementation!r}")
        if source_gate not in GATE:
            fail(f"{task_id}.source_gate has invalid value {source_gate!r}")
        if runtime_gate not in GATE:
            fail(f"{task_id}.runtime_gate has invalid value {runtime_gate!r}")
        if overall not in OVERALL:
            fail(f"{task_id}.overall has invalid value {overall!r}")
        if overall == "completed":
            if implementation != "completed" or source_gate != "passed":
                fail(f"{task_id} completed requires completed implementation and passed source gate")
            if runtime_gate not in {"passed", "not_required"}:
                fail(f"{task_id} completed requires passed/not_required runtime gate")
        if overall == "pending_runtime_validation":
            if implementation != "completed" or source_gate != "passed" or runtime_gate != "pending":
                fail(f"{task_id} pending_runtime_validation state is inconsistent")
        if overall == "pending" and implementation != "pending":
            fail(f"{task_id} pending requires pending implementation")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("path", nargs="?", default="docs/development/oneshot/task-state.yaml")
    args = parser.parse_args()
    try:
        validate(pathlib.Path(args.path))
    except Exception as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        return 1
    print("One-shot task state: PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
