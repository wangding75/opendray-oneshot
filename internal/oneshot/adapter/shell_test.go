package adapter

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

func TestShellAdapterDisabledByDefault(t *testing.T) {
	adapter := NewShellAdapter(ShellConfig{})
	_, err := adapter.BuildCommand(context.Background(), ExecutionInput{CommandName: "success"})
	if !domain.HasCode(err, domain.ErrorDisabled) {
		t.Fatalf("disabled adapter error = %v", err)
	}
}

func testExecutable() string {
	if filepath.Separator == '\\' {
		return `C:\bin\sh`
	}
	return "/bin/sh"
}

func TestShellAdapterRequiresAllowlistedCommand(t *testing.T) {
	cwd := t.TempDir()
	adapter := NewShellAdapter(ShellConfig{
		Enabled: true,
		Commands: map[string]CommandSpec{
			"success": {Executable: testExecutable(), Args: []string{"fixture.sh"}, Dir: cwd},
		},
	})
	_, err := adapter.BuildCommand(context.Background(), ExecutionInput{CommandName: "rm -rf /"})
	if !domain.HasCode(err, domain.ErrorInvalidRequest) {
		t.Fatalf("non-allowlisted command error = %v", err)
	}
}

func TestShellAdapterEnvironmentAllowlistAndRedaction(t *testing.T) {
	cwd := t.TempDir()
	adapter := NewShellAdapter(ShellConfig{
		Enabled: true,
		Commands: map[string]CommandSpec{
			"success": {
				Executable: testExecutable(),
				Args:       []string{"fixture.sh"},
				Dir:        cwd,
				Environment: map[string]EnvironmentValue{
					"BASE": {Value: "base"},
				},
			},
		},
		AllowedEnvironment: []string{"VISIBLE"},
		SecretEnvironment:  []string{"TOKEN"},
	})

	spec, err := adapter.BuildCommand(context.Background(), ExecutionInput{
		CommandName: "success",
		Environment: map[string]string{"VISIBLE": "value", "TOKEN": "top-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Environment["TOKEN"].Value != "top-secret" || !spec.Environment["TOKEN"].Secret {
		t.Fatalf("raw secret environment not retained for child: %+v", spec.Environment["TOKEN"])
	}
	redacted := spec.Redacted()
	if got := redacted.Environment["TOKEN"]; got != "[REDACTED]" {
		t.Fatalf("redacted secret = %q", got)
	}
	if got := redacted.Environment["VISIBLE"]; got != "value" {
		t.Fatalf("visible environment = %q", got)
	}
	if got := spec.ProcessEnvironment(); !reflect.DeepEqual(got, []string{"BASE=base", "TOKEN=top-secret", "VISIBLE=value"}) {
		t.Fatalf("process environment = %#v", got)
	}

	_, err = adapter.BuildCommand(context.Background(), ExecutionInput{
		CommandName: "success",
		Environment: map[string]string{"NOT_ALLOWED": "value"},
	})
	if !domain.HasCode(err, domain.ErrorInvalidRequest) {
		t.Fatalf("unexpected environment error = %v", err)
	}
}

func TestShellAdapterReturnsDefensiveCommandCopy(t *testing.T) {
	cwd := t.TempDir()
	original := CommandSpec{
		Executable: testExecutable(),
		Args:       []string{"fixture.sh"},
		Dir:        cwd,
		Environment: map[string]EnvironmentValue{
			"A": {Value: "one"},
		},
	}
	adapter := NewShellAdapter(ShellConfig{Enabled: true, Commands: map[string]CommandSpec{"success": original}})
	first, err := adapter.BuildCommand(context.Background(), ExecutionInput{CommandName: "success"})
	if err != nil {
		t.Fatal(err)
	}
	first.Args[0] = "mutated"
	first.Environment["A"] = EnvironmentValue{Value: "mutated"}
	second, err := adapter.BuildCommand(context.Background(), ExecutionInput{CommandName: "success"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Args[0] != "fixture.sh" || second.Environment["A"].Value != "one" {
		t.Fatalf("adapter command mutated: %+v", second)
	}
}

func TestShellAdapterRejectsRelativeExecutableAndCWD(t *testing.T) {
	adapter := NewShellAdapter(ShellConfig{Enabled: true, Commands: map[string]CommandSpec{
		"relative-executable": {Executable: "sh", Dir: t.TempDir()},
		"relative-cwd":        {Executable: testExecutable(), Dir: "."},
	}})
	for _, name := range []string{"relative-executable", "relative-cwd"} {
		_, err := adapter.BuildCommand(context.Background(), ExecutionInput{CommandName: name})
		if !domain.HasCode(err, domain.ErrorInvalidRequest) {
			t.Fatalf("%s error = %v", name, err)
		}
	}
}

func TestFixtureExecutableExistsOnUnix(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fixture")
	}
	if !filepath.IsAbs("/bin/sh") {
		t.Fatal("fixture executable is not absolute")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Fatalf("/bin/sh unavailable: %v", err)
	}
}

func TestShellAdapterNormalizesPassthroughOutput(t *testing.T) {
	adapter := NewShellAdapter(ShellConfig{Enabled: true})
	text := "hello"
	events, err := adapter.NormalizeOutput(context.Background(), OutputChunk{
		RunID: "orn_output", Sequence: 3, Stream: domain.StreamStdout,
		ByteOffset: 8, ByteLength: 5, StreamRecordID: "osr_output",
		RawArtifactID: "oar_output", DecodeStatus: domain.DecodeValidUTF8,
		Text: &text, SHA256: strings.Repeat("a", 64), ReceivedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "shell.passthrough" {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Content["text"] != "hello" || events[0].Content["stream_record_id"] != "osr_output" {
		t.Fatalf("content = %+v", events[0].Content)
	}
	if adapter.AdapterVersion() != ShellAdapterVersion {
		t.Fatalf("adapter version = %q", adapter.AdapterVersion())
	}
}
