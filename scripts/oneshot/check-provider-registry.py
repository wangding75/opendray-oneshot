#!/usr/bin/env python3
from pathlib import Path
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


types = read("internal/oneshot/adapter/types.go")
registry = read("internal/oneshot/adapter/registry.go")
shell = read("internal/oneshot/adapter/shell.go")
tests = read("internal/oneshot/adapter/registry_test.go")

for capability in ("SupportsNonInteractive", "SupportsResume", "StructuredOutput", "Attachments", "Cancellation"):
    if capability not in types or capability not in tests + shell:
        fail(f"provider capability {capability} is missing")
for boundary in ("ProviderCatalog", "CredentialAllocator", "ProviderMetadata", "CredentialLease", "ProviderDescriptor"):
    if boundary not in types + registry:
        fail(f"provider boundary {boundary} is missing")
for token in ("ErrorRunConflict", "ErrorUnsupportedProvider", "ErrorDisabled", "MinimumProviderVersion", "compareVersions"):
    if token not in registry:
        fail(f"stable registry behavior {token} is missing")
if "SupportsNonInteractive" not in shell or "MinimumProviderVersion" not in shell:
    fail("Shell fixture does not implement the provider capability contract")
for forbidden in ("internal/session", "github.com/creack/pty", "pty.Start", "SessionID"):
    if forbidden in types + registry + shell:
        fail(f"provider registry contains forbidden interactive dependency: {forbidden}")
for test_name in (
    "TestRegistryRegisterResolveAndExposeCapabilities",
    "TestRegistryRejectsDuplicateUnknownUnsupportedAndDisabled",
    "TestRegistryRejectsProviderDisabledAndVersionMismatch",
    "TestRegistryCredentialAllocationUsesNarrowBoundary",
):
    if test_name not in tests:
        fail(f"missing provider registry test {test_name}")
print("PASS: OD-OS-14 provider capability descriptor, versioned adapter registry, shared metadata and credential boundary")
