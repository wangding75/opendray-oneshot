package workspacepolicy

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

// WorkspacePolicyError bridges WorkspacePolicy errors to DomainError codes.
type WorkspacePolicyError struct {
	Code    domain.ErrorCode
	Message string
	Cause   error
}

func (e *WorkspacePolicyError) Error() string {
	return e.Message
}

func (e *WorkspacePolicyError) Unwrap() error {
	return e.Cause
}

func (e *WorkspacePolicyError) As(target any) bool {
	if de, ok := target.(**domain.DomainError); ok {
		*de = domain.NewDomainError(e.Code, e.Message, e.Cause)
		return true
	}
	return false
}

func (e *WorkspacePolicyError) Is(target error) bool {
	if target == ErrWorkspaceOutsideAllowedRoots {
		return e.Code == domain.ErrorForbidden && e.Message == "workspace_path is outside the allowed workspace roots"
	}
	if target == ErrWorkspaceNotConfigured {
		return e.Code == domain.ErrorInvalidRequest && e.Message == "no allowed workspace root is configured"
	}
	if target == ErrInvalidWorkspace {
		return e.Code == domain.ErrorInvalidRequest
	}
	if we, ok := target.(*WorkspacePolicyError); ok {
		return e.Code == we.Code && (we.Message == "" || e.Message == we.Message)
	}
	return false
}

var (
	ErrWorkspaceNotConfigured = &WorkspacePolicyError{
		Code:    domain.ErrorInvalidRequest,
		Message: "no allowed workspace root is configured",
	}
	ErrWorkspaceOutsideAllowedRoots = &WorkspacePolicyError{
		Code:    domain.ErrorForbidden,
		Message: "workspace_path is outside the allowed workspace roots",
	}
	ErrInvalidWorkspace = &WorkspacePolicyError{
		Code: domain.ErrorInvalidRequest,
	}
)

// Policy centralizes One-shot workspace authorization. It canonicalizes roots
// once at startup and applies the same containment checks during Task creation
// and process start.
type Policy struct {
	roots []string
}

func New(roots []string) (*Policy, error) {
	canonical := make([]string, 0, len(roots))
	seen := map[string]struct{}{}
	for _, raw := range roots {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		root, err := canonicalizeExistingDirectory(trimmed)
		if err != nil {
			return nil, domain.NewDomainError(domain.ErrorInvalidRequest, "invalid allowed One-shot workspace root", fmt.Errorf("%s: %w", trimmed, err))
		}
		if isFilesystemRoot(root) {
			return nil, domain.InvalidRequestf("allowed One-shot workspace roots must not be a filesystem root: %s", root)
		}
		key := comparisonKey(root)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		canonical = append(canonical, root)
	}
	return &Policy{roots: canonical}, nil
}

func (p *Policy) Roots() []string {
	if p == nil || len(p.roots) == 0 {
		return nil
	}
	return append([]string(nil), p.roots...)
}

func (p *Policy) HasRoots() bool { return p != nil && len(p.roots) > 0 }

func (p *Policy) NormalizeAndValidate(path string) (string, error) {
	if p == nil || len(p.roots) == 0 {
		return "", ErrWorkspaceNotConfigured
	}
	candidate, err := canonicalizeExistingDirectory(path)
	if err != nil {
		return "", err
	}
	for _, root := range p.roots {
		ok, relErr := withinRoot(root, candidate)
		if relErr != nil {
			continue
		}
		if ok {
			return candidate, nil
		}
	}
	return "", ErrWorkspaceOutsideAllowedRoots
}

func canonicalizeExistingDirectory(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", &WorkspacePolicyError{
			Code:    domain.ErrorInvalidRequest,
			Message: "workspace_path is required",
		}
	}
	if !filepath.IsAbs(path) {
		return "", &WorkspacePolicyError{
			Code:    domain.ErrorInvalidRequest,
			Message: "workspace_path must be absolute",
		}
	}
	cleaned := filepath.Clean(path)
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", &WorkspacePolicyError{
			Code:    domain.ErrorInvalidRequest,
			Message: "workspace_path is invalid",
			Cause:   err,
		}
	}
	evaluated, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", &WorkspacePolicyError{
				Code:    domain.ErrorInvalidRequest,
				Message: "workspace_path does not exist",
				Cause:   err,
			}
		}
		return "", &WorkspacePolicyError{
			Code:    domain.ErrorInvalidRequest,
			Message: "workspace_path is invalid",
			Cause:   err,
		}
	}
	info, err := os.Stat(evaluated)
	if err != nil {
		if os.IsNotExist(err) {
			return "", &WorkspacePolicyError{
				Code:    domain.ErrorInvalidRequest,
				Message: "workspace_path does not exist",
				Cause:   err,
			}
		}
		return "", &WorkspacePolicyError{
			Code:    domain.ErrorInvalidRequest,
			Message: "workspace_path is invalid",
			Cause:   err,
		}
	}
	if !info.IsDir() {
		return "", &WorkspacePolicyError{
			Code:    domain.ErrorInvalidRequest,
			Message: "workspace_path must reference a directory",
		}
	}
	return filepath.Clean(evaluated), nil
}

func withinRoot(root, candidate string) (bool, error) {
	if comparisonKey(root) == comparisonKey(candidate) {
		return true, nil
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, err
	}
	if rel == "." {
		return true, nil
	}
	if filepath.IsAbs(rel) {
		return false, nil
	}
	if rel == ".." {
		return false, nil
	}
	prefix := ".." + string(filepath.Separator)
	if strings.HasPrefix(rel, prefix) {
		return false, nil
	}
	return true, nil
}

func isFilesystemRoot(path string) bool {
	cleaned := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		volume := filepath.VolumeName(cleaned)
		if volume == "" {
			return false
		}
		remainder := strings.TrimPrefix(cleaned, volume)
		return remainder == string(filepath.Separator)
	}
	return cleaned == string(filepath.Separator)
}

func comparisonKey(path string) string {
	cleaned := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}
