#!/usr/bin/env python3
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[2]


def fail(message: str) -> None:
    print(f"FAIL: {message}", file=sys.stderr)
    raise SystemExit(1)


def read(path: str) -> str:
    target = ROOT / path
    if not target.is_file():
        fail(f"missing {path}")
    return target.read_text(encoding="utf-8")


core = read("internal/oneshot/executor/process_supervisor.go")
linux = read("internal/oneshot/executor/process_supervisor_linux.go")
unix = read("internal/oneshot/executor/process_supervisor_unix.go")
windows = read("internal/oneshot/executor/process_supervisor_windows.go")
tests = read("internal/oneshot/executor/process_supervisor_test.go")

for method in ("Start", "TerminateTree", "KillTree", "Wait", "IsAlive", "TerminateExistingTree"):
    if not re.search(rf"func .*\b{method}\(", core):
        fail(f"ProcessSupervisor method {method} is missing")
for token in ("Setpgid: true", "syscall.Kill(-pid, syscall.SIGTERM)", "syscall.Kill(-pid, syscall.SIGKILL)"):
    if token not in linux and token not in unix:
        fail(f"Unix process-tree primitive is missing: {token}")
if "use WSL2/Linux" not in windows or "ProcessTree: false" not in windows:
    fail("native Windows unsupported capability is not explicit")
if "cmd.Wait()" not in core or "stdout/stderr copying has drained" not in core:
    fail("Wait/drain ownership is not explicit")
if "os.Pipe()" not in core or "copyOutput" not in core:
    fail("explicit output pipes are required so leader exit cannot block on descendants")
if "terminationTimeout" not in core or "TerminateTree" not in core:
    fail("timeout and cancel do not share process-tree cleanup")
for forbidden in ("internal/session", "github.com/creack/pty", "pty.Start"):
    if forbidden in core + linux + unix + windows:
        fail(f"ProcessSupervisor contains forbidden dependency: {forbidden}")
for test_name in (
    "TestProcessSupervisorTerminatesMultiLevelTreeAndPreservesOutput",
    "TestProcessSupervisorTimeoutUsesSameTreeCleanup",
    "TestProcessSupervisorTERMIgnoreFallsBackToKILL",
    "TestProcessSupervisorNaturalExitDrainsOutput",
    "TestProcessSupervisorNaturalLeaderExitCleansBackgroundDescendant",
    "TestTerminateExistingTreeIsIdempotent",
):
    if test_name not in tests:
        fail(f"missing ProcessSupervisor regression test {test_name}")
print("PASS: OD-OS-12 process-tree supervision, TERM/KILL cleanup, timeout, output drain and idempotent cancellation")
