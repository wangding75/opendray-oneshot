#!/usr/bin/env python3
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[2]


def fail(message: str) -> None:
    print(f"FAIL: {message}", file=sys.stderr)
    raise SystemExit(1)


def require(path: str) -> str:
    file_path = ROOT / path
    if not file_path.is_file():
        fail(f"missing {path}")
    return file_path.read_text(encoding="utf-8")


types = require("internal/oneshot/adapter/types.go")
shell = require("internal/oneshot/adapter/shell.go")
process = require("internal/oneshot/executor/process.go")
supervisor = require("internal/oneshot/executor/process_supervisor.go")
service = require("internal/oneshot/executor/run_service.go")
store = require("internal/oneshot/store/run.go")
adapter_tests = require("internal/oneshot/adapter/shell_test.go")
executor_tests = require("internal/oneshot/executor/process_test.go")
service_tests = require("internal/oneshot/executor/run_service_test.go")
require("internal/oneshot/testdata/fixtures/success.sh")
require("internal/oneshot/testdata/fixtures/nonzero.sh")

for name in ("CommandSpec", "ExecutionInput", "ExecutionResult", "OneShotAdapter"):
    if not re.search(rf"type\s+{name}\b", types):
        fail(f"adapter contract {name} is missing")

if "ProcessEnvironment" not in types or "[REDACTED]" not in types:
    fail("explicit environment construction or secret redaction is missing")
if "CommandName" not in types:
    fail("execution input does not use an allowlisted command name")
if "commands[name]" not in shell or "test allowlist" not in shell:
    fail("Shell adapter command allowlist is missing")
if "if !a.Enabled()" not in shell or "ErrorDisabled" not in shell:
    fail("Shell adapter is not disabled by default")
if "allowedEnv" not in shell or "secretEnv" not in shell:
    fail("Shell adapter environment allowlist or secret classification is missing")
if "exec.Command(" not in supervisor:
    fail("ordinary os/exec launch is missing")
if "os.Stdin" in supervisor:
    fail("executor unexpectedly accepts interactive terminal stdin")
if "bytes.NewReader(spec.Stdin)" not in supervisor:
    fail("executor does not support bounded prompt stdin from CommandSpec")
if "CreateRunWithSaga" not in service or "ProcessStarted" not in service or "ProcessExited" not in service:
    fail("Run lifecycle persistence is incomplete")
if "FinalizeRunWithTask" not in service or "FinalizeRunWithTask" not in store:
    fail("atomic terminal Run/Task finalization is missing")
if "NewWorkerWhenEnabled" not in service or "if !enabled" not in service:
    fail("disabled One-shot worker gate is missing")

combined = "\n".join((types, shell, process, supervisor, service))
for forbidden in (
    "github.com/creack/pty",
    "internal/session",
    "pty.Start",
    "INSERT INTO sessions",
    "UPDATE sessions",
    "session_transcripts",
):
    if forbidden in combined:
        fail(f"executor boundary contains forbidden token: {forbidden}")

required_tests = (
    "TestShellAdapterDisabledByDefault",
    "TestShellAdapterRequiresAllowlistedCommand",
    "TestShellAdapterEnvironmentAllowlistAndRedaction",
    "TestProcessExecutorSuccessAndNonZeroExit",
    "TestProcessExecutorCommandAndCWDFailures",
    "TestWorkerRunServiceExecutorChain",
    "TestNewWorkerWhenDisabledDoesNotStartWorker",
)
all_tests = adapter_tests + executor_tests + service_tests
for test_name in required_tests:
    if test_name not in all_tests:
        fail(f"required test {test_name} is missing")

print("PASS: adapter contracts, Shell fixture allowlist, ordinary process executor, Run lifecycle and disabled worker gate")
print("PASS: executor has no PTY, Session, sessions-table or session_transcripts dependency")
