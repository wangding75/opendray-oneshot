package workspacepolicy

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
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
		return "", domain.InvalidRequestf("no allowed One-shot workspace roots are configured")
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
	return "", domain.NewDomainError(domain.ErrorForbidden, "workspace_path is outside the allowed One-shot workspace roots", nil)
}

func canonicalizeExistingDirectory(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", domain.InvalidRequestf("workspace_path is required")
	}
	if !filepath.IsAbs(path) {
		return "", domain.InvalidRequestf("workspace_path must be absolute")
	}
	cleaned := filepath.Clean(path)
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", domain.NewDomainError(domain.ErrorExecutionFailed, "failed to resolve workspace_path", err)
	}
	evaluated, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", domain.NewDomainError(domain.ErrorExecutionFailed, "workspace_path is unavailable", err)
	}
	info, err := os.Stat(evaluated)
	if err != nil {
		return "", domain.NewDomainError(domain.ErrorExecutionFailed, "workspace_path is unavailable", err)
	}
	if !info.IsDir() {
		return "", domain.InvalidRequestf("workspace_path must be a directory")
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
