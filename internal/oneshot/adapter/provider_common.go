package adapter

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

var windowsAbsolutePathPattern = regexp.MustCompile(`^[A-Za-z]:[\\/]|^\\\\[^\\]+\\[^\\]+`)

type providerRunState struct {
	buffer            string
	providerContextID string
	resume            bool
	errorText         strings.Builder
}

type providerStateStore struct {
	mu   sync.Mutex
	runs map[string]*providerRunState
}

func (s *providerStateStore) withState(runID string, fn func(*providerRunState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs == nil {
		s.runs = make(map[string]*providerRunState)
	}
	state := s.runs[runID]
	if state == nil {
		state = &providerRunState{}
		s.runs[runID] = state
	}
	fn(state)
}

func (s *providerStateStore) prepare(runID string, resume bool) {
	s.withState(runID, func(state *providerRunState) {
		state.resume = resume
	})
}

func (s *providerStateStore) isResume(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.runs[runID]
	return state != nil && state.resume
}

func (s *providerStateStore) evidence(runID string) (RuntimeContextEvidence, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.runs[runID]
	if state == nil || strings.TrimSpace(state.providerContextID) == "" {
		return RuntimeContextEvidence{}, false
	}
	return RuntimeContextEvidence{ProviderContextID: strings.TrimSpace(state.providerContextID)}, true
}

func (s *providerStateStore) errorText(runID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.runs[runID]
	if state == nil {
		return ""
	}
	return state.errorText.String()
}

func (s *providerStateStore) forget(runID string) {
	s.mu.Lock()
	delete(s.runs, runID)
	s.mu.Unlock()
}

func providerWorkspace(input ExecutionInput) (string, error) {
	var workspace string
	if input.RuntimeContext != nil {
		workspace = strings.TrimSpace(input.RuntimeContext.WorkspacePath)
	}
	if value, ok := input.Delivery.Input.Options["workspace_path"]; ok {
		requested, ok := value.(string)
		if !ok {
			return "", domain.InvalidRequestf("workspace_path must be a string")
		}
		requested = strings.TrimSpace(requested)
		if workspace != "" && requested != workspace {
			return "", domain.NewDomainError(domain.ErrorContextOwnerMismatch, "resume workspace does not match RuntimeContext", nil)
		}
		workspace = requested
	}
	if workspace == "" {
		return "", domain.InvalidRequestf("workspace_path is required")
	}
	if !filepath.IsAbs(workspace) && !windowsAbsolutePathPattern.MatchString(workspace) {
		return "", domain.InvalidRequestf("workspace_path must be absolute")
	}
	return workspace, nil
}

func providerPrompt(input ExecutionInput) (string, error) {
	prompt := input.Prompt
	if input.Delivery.Operation == domain.DeliveryContinue {
		prompt = input.Delivery.Input.PromptDelta
	}
	if strings.TrimSpace(prompt) == "" && len(input.Delivery.Input.AttachmentRefs) == 0 {
		return "", domain.InvalidRequestf("provider prompt is required")
	}
	return prompt, nil
}

func buildProviderEnvironment(input map[string]string, allowed, secret map[string]struct{}) (map[string]EnvironmentValue, error) {
	out := make(map[string]EnvironmentValue, len(input))
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := allowed[key]; !ok {
			return nil, domain.InvalidRequestf("environment variable %q is not allowed", key)
		}
		_, isSecret := secret[key]
		out[key] = EnvironmentValue{Value: input[key], Secret: isSecret}
	}
	return out, nil
}

func environmentSets(allowed, secret []string) (map[string]struct{}, map[string]struct{}) {
	allowedSet := make(map[string]struct{}, len(allowed)+len(secret))
	secretSet := make(map[string]struct{}, len(secret))
	for _, key := range allowed {
		if key = strings.TrimSpace(key); key != "" {
			allowedSet[key] = struct{}{}
		}
	}
	for _, key := range secret {
		if key = strings.TrimSpace(key); key != "" {
			allowedSet[key] = struct{}{}
			secretSet[key] = struct{}{}
		}
	}
	return allowedSet, secretSet
}

func decodeJSONLines(store *providerStateStore, chunk OutputChunk, provider string, handle func(map[string]any, *providerRunState) (string, map[string]any)) ([]NormalizedOutputEvent, error) {
	if chunk.Text == nil || *chunk.Text == "" {
		return []NormalizedOutputEvent{{Type: provider + ".raw", Content: rawChunkContent(chunk), OccurredAt: chunk.ReceivedAt}}, nil
	}
	var events []NormalizedOutputEvent
	store.withState(chunk.RunID, func(state *providerRunState) {
		if chunk.Stream == domain.StreamStderr {
			state.errorText.WriteString(*chunk.Text)
			events = append(events, NormalizedOutputEvent{Type: provider + ".stderr", Content: rawChunkContent(chunk), OccurredAt: chunk.ReceivedAt})
			return
		}
		state.buffer += *chunk.Text
		for {
			index := strings.IndexByte(state.buffer, '\n')
			if index < 0 {
				break
			}
			line := strings.TrimSpace(state.buffer[:index])
			state.buffer = state.buffer[index+1:]
			if line == "" {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				state.errorText.WriteString(line)
				state.errorText.WriteByte('\n')
				events = append(events, NormalizedOutputEvent{Type: provider + ".unparsed", Content: map[string]any{"text": line, "stream_record_id": chunk.StreamRecordID}, OccurredAt: chunk.ReceivedAt})
				continue
			}
			typeName, content := handle(payload, state)
			if typeName == "" {
				typeName = provider + ".unknown"
			}
			if content == nil {
				content = payload
			}
			content["stream_record_id"] = chunk.StreamRecordID
			events = append(events, NormalizedOutputEvent{Type: typeName, Content: content, OccurredAt: chunk.ReceivedAt})
		}
	})
	return events, nil
}

func rawChunkContent(chunk OutputChunk) map[string]any {
	content := map[string]any{
		"stream": chunk.Stream.String(), "stream_sequence": chunk.Sequence,
		"stream_record_id": chunk.StreamRecordID, "raw_artifact_id": chunk.RawArtifactID,
		"byte_offset": chunk.ByteOffset, "byte_length": chunk.ByteLength,
		"decode_status": chunk.DecodeStatus.String(), "sha256": chunk.SHA256,
	}
	if chunk.Text != nil {
		content["text"] = *chunk.Text
	}
	return content
}

func classifyProviderFailure(provider string, resume bool, text string, result ExecutionResult) ExecutionResult {
	if result.Err == nil && result.ExitCode == 0 {
		return result
	}
	lower := strings.ToLower(text)
	code := domain.ErrorExecutionFailed
	message := strings.TrimSpace(text)
	if message == "" {
		message = provider + " process failed"
	}
	switch {
	case strings.Contains(lower, "rate limit") || strings.Contains(lower, "rate_limit") || strings.Contains(lower, "too many requests") || strings.Contains(lower, "429"):
		code = domain.ErrorRateLimited
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "authentication") || strings.Contains(lower, "not logged in") || strings.Contains(lower, "invalid api key") || strings.Contains(lower, "401"):
		code = domain.ErrorUnauthorized
	case strings.Contains(lower, "permission denied") || strings.Contains(lower, "forbidden") || strings.Contains(lower, "approval required") || strings.Contains(lower, "403"):
		code = domain.ErrorForbidden
	case resume && (strings.Contains(lower, "session") || strings.Contains(lower, "thread") || strings.Contains(lower, "resume")):
		code = domain.ErrorResumeFailed
	}
	result.Err = domain.NewDomainError(code, message, result.Err)
	return result
}

func contextEvidence(ctx context.Context, store *providerStateStore, runID string) (RuntimeContextEvidence, bool, error) {
	if err := ctx.Err(); err != nil {
		return RuntimeContextEvidence{}, false, err
	}
	evidence, ok := store.evidence(runID)
	return evidence, ok, nil
}
