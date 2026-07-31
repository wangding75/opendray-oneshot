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


collector = require("internal/oneshot/executor/output_collector.go")
reader = require("internal/oneshot/executor/stream_reader.go")
process = require("internal/oneshot/executor/process.go")
service = require("internal/oneshot/executor/run_service.go")
adapter_types = require("internal/oneshot/adapter/types.go")
shell = require("internal/oneshot/adapter/shell.go")
store = require("internal/oneshot/store/output.go")
tests = require("internal/oneshot/executor/output_collector_test.go")
service_tests = require("internal/oneshot/executor/run_service_test.go")
adapter_tests = require("internal/oneshot/adapter/shell_test.go")
require("internal/oneshot/testdata/fixtures/interleaved.sh")

for symbol in (
    "OutputCursor", "OutputRepository", "ArtifactStorage", "FileArtifactStorage",
    "OutputCollector", "OutputCollectorConfig", "FinalOutput",
):
    if not re.search(rf"type\s+{symbol}\b", collector):
        fail(f"missing output type {symbol}")

for token in (
    "sync.Mutex", "StreamSequence", "EventSequence", "StdoutOffset", "StderrOffset",
    "sha256.Sum256", "ArtifactRawStdout", "ArtifactRawStderr", "ArtifactFinalResult",
    "DecodeValidUTF8", "DecodeLossyUTF8", "DecodeBinary",
    "LoadOutputCursor", "AppendOutput", "NormalizeOutput", "Finalize",
):
    if token not in collector:
        fail(f"collector missing required behavior token {token}")

if "defaultOutputChunkSize" not in collector or "chunkSize" not in reader:
    fail("bounded streaming chunk split is missing")
if "context.WithoutCancel" not in service:
    fail("output drain context is not isolated from process cancellation")
if "StartWithOutput" not in process or "StartWithOutput" not in service:
    fail("Run-scoped stdout/stderr writer integration is missing")
if "shell.passthrough" not in shell or "AdapterVersion" not in adapter_types:
    fail("Shell passthrough StandardEvent normalization is missing")
if "PersistOutputBatch" not in store or "LoadOutputCursor" not in store or "AppendOutput" not in store:
    fail("output metadata persistence/cursor recovery is incomplete")
if "MAX(sr.byte_offset + sr.byte_length)" not in store:
    fail("per-stream byte offset recovery query is missing")
if "replay_events_url" not in collector or "stream_record_run_id" not in collector:
    fail("final result cannot trace persisted raw StreamRecords")

required_tests = (
    "TestOutputCollectorOrdersInterleavedStreamsAndPersistsRawBytes",
    "TestOutputCollectorHandlesNoNewlineLargeOutputWithinChunkBound",
    "TestOutputCollectorDecodesUTF8AcrossChunksWithoutLosingRawBytes",
    "TestOutputCollectorPreservesInvalidBinaryBytes",
    "TestOutputCollectorResumesSequencesAndFinalArtifactReferencesRawRecords",
    "TestRunServiceCapturesInterleavedOutputAndFinalManifest",
    "TestShellAdapterNormalizesPassthroughOutput",
    "TestFileArtifactStorageRejectsTraversalAndOverwrite",
    "TestOutputCollectorPersistenceFailureStopsFurtherWrites",
)
all_tests = tests + service_tests + adapter_tests
for test_name in required_tests:
    if test_name not in all_tests:
        fail(f"required test {test_name} is missing")

combined = "\n".join((collector, reader, process, service, shell, store))
for forbidden in (
    "github.com/creack/pty",
    "internal/session",
    "pty.Start",
    "session_transcripts",
    "SessionID",
    "ring buffer",
):
    if forbidden in combined:
        fail(f"One-shot output path crosses Interactive boundary with {forbidden}")

print("PASS: ordered stdout/stderr collection, bounded streaming, raw Artifact storage and UTF-8/binary separation")
print("PASS: Shell passthrough StandardEvents, durable sequence/offset recovery and final-result traceability")
print("PASS: output path has no PTY, Session transcript, SessionID or ring-buffer dependency")
