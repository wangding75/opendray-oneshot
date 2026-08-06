package workspacepolicy

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

func TestWorkspacePolicyErrorContract(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Relative path
	_, err = policy.NormalizeAndValidate("relative")
	if !errors.Is(err, ErrInvalidWorkspace) {
		t.Errorf("expected ErrInvalidWorkspace, got %v", err)
	}
	if gotCode, ok := domain.CodeOf(err); !ok || gotCode != domain.ErrorInvalidRequest {
		t.Errorf("expected ErrorInvalidRequest code, got %s", gotCode)
	}

	// 2. Path does not exist
	missing := filepath.Join(root, "missing")
	_, err = policy.NormalizeAndValidate(missing)
	if !errors.Is(err, ErrInvalidWorkspace) {
		t.Errorf("expected ErrInvalidWorkspace, got %v", err)
	}
	if gotCode, ok := domain.CodeOf(err); !ok || gotCode != domain.ErrorInvalidRequest {
		t.Errorf("expected ErrorInvalidRequest code, got %s", gotCode)
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected error message to contain 'does not exist', got %v", err)
	}

	// 3. Ordinary file (not directory)
	_, err = policy.NormalizeAndValidate(file)
	if !errors.Is(err, ErrInvalidWorkspace) {
		t.Errorf("expected ErrInvalidWorkspace, got %v", err)
	}
	if gotCode, ok := domain.CodeOf(err); !ok || gotCode != domain.ErrorInvalidRequest {
		t.Errorf("expected ErrorInvalidRequest code, got %s", gotCode)
	}
	if !strings.Contains(err.Error(), "reference a directory") {
		t.Errorf("expected error message to contain 'reference a directory', got %v", err)
	}

	// 4. Outside allowed roots
	outside := t.TempDir()
	_, err = policy.NormalizeAndValidate(outside)
	if !errors.Is(err, ErrWorkspaceOutsideAllowedRoots) {
		t.Errorf("expected ErrWorkspaceOutsideAllowedRoots, got %v", err)
	}
	if gotCode, ok := domain.CodeOf(err); !ok || gotCode != domain.ErrorForbidden {
		t.Errorf("expected ErrorForbidden code, got %s", gotCode)
	}

	// 5. Unconfigured allowed roots
	emptyPolicy, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = emptyPolicy.NormalizeAndValidate(root)
	if !errors.Is(err, ErrWorkspaceNotConfigured) {
		t.Errorf("expected ErrWorkspaceNotConfigured, got %v", err)
	}
	if gotCode, ok := domain.CodeOf(err); !ok || gotCode != domain.ErrorInvalidRequest {
		t.Errorf("expected ErrorInvalidRequest code, got %s", gotCode)
	}

	// Test As, Is, HasCode and Cause preservation explicitly
	t.Run("error classification behavior", func(t *testing.T) {
		cause := errors.New("underlying filesystem error")
		werr := &WorkspacePolicyError{
			Code:    domain.ErrorInvalidRequest,
			Message: "custom workspace error",
			Cause:   cause,
		}

		// 1. errors.As behavior
		var de *domain.DomainError
		if !errors.As(werr, &de) {
			t.Fatal("expected errors.As to succeed for WorkspacePolicyError")
		}
		if de.Code != domain.ErrorInvalidRequest {
			t.Errorf("expected code %s, got %s", domain.ErrorInvalidRequest, de.Code)
		}
		if de.Message != "custom workspace error" {
			t.Errorf("expected message 'custom workspace error', got %q", de.Message)
		}
		if de.Cause != cause {
			t.Errorf("expected cause to be preserved, got %v", de.Cause)
		}

		// 2. domain.HasCode behavior
		if !domain.HasCode(werr, domain.ErrorInvalidRequest) {
			t.Error("expected domain.HasCode to report true for ErrorInvalidRequest")
		}
		if domain.HasCode(werr, domain.ErrorForbidden) {
			t.Error("expected domain.HasCode to report false for ErrorForbidden")
		}

		// 3. errors.Is behavior
		if !errors.Is(werr, ErrInvalidWorkspace) {
			t.Error("expected errors.Is to match ErrInvalidWorkspace")
		}

		// A non-workspace error with ErrorInvalidRequest must NOT be identified as a workspace error
		nonWorkspaceErr := domain.NewDomainError(domain.ErrorInvalidRequest, "some other invalid request", nil)
		if errors.Is(nonWorkspaceErr, ErrInvalidWorkspace) {
			t.Error("expected errors.Is NOT to match ErrInvalidWorkspace for general DomainError")
		}
		if errors.Is(nonWorkspaceErr, ErrWorkspaceNotConfigured) {
			t.Error("expected errors.Is NOT to match ErrWorkspaceNotConfigured for general DomainError")
		}
	})
}

func TestAllowedRootInitializationValidAndDedup(t *testing.T) {
	root := t.TempDir()
	dup := filepath.Clean(root)
	policy, err := New([]string{" ", root, dup})
	if err != nil {
		t.Fatal(err)
	}
	if got := policy.Roots(); len(got) != 1 || got[0] != filepath.Clean(root) {
		t.Fatalf("roots=%v", got)
	}
}

func TestAllowedRootInitializationRejectsInvalidRoots(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	for _, roots := range [][]string{
		{"relative"},
		{missing},
		{file},
	} {
		if _, err := New(roots); err == nil {
			t.Fatalf("roots=%v accepted", roots)
		}
	}
	rootPath := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		rootPath = os.Getenv("SystemDrive") + `\`
	}
	if _, err := New([]string{rootPath}); err == nil {
		t.Fatalf("filesystem root accepted: %s", rootPath)
	}
}

func TestWorkspacePathAllowsRootAndChildren(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child", "nested space")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	policy, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{root, filepath.Join(root, "child"), child} {
		got, err := policy.NormalizeAndValidate(candidate)
		if err != nil {
			t.Fatalf("candidate=%s err=%v", candidate, err)
		}
		if got != filepath.Clean(candidate) {
			t.Fatalf("candidate=%s got=%s", candidate, got)
		}
	}
}

func TestWorkspacePathRejectsSiblingPrefixParentTraversalAndFiles(t *testing.T) {
	rootParent := t.TempDir()
	root := filepath.Join(rootParent, "work")
	sibling := filepath.Join(rootParent, "work-other")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{
		sibling,
		filepath.Join(root, ".."),
		filepath.Join(root, "child", "..", ".."),
		file,
		filepath.Join(root, "missing"),
		"relative",
	} {
		if _, err := policy.NormalizeAndValidate(candidate); err == nil {
			t.Fatalf("candidate=%s accepted", candidate)
		}
	}
}

func TestWorkspacePathRejectsEmptyRootsConfiguration(t *testing.T) {
	policy, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.NormalizeAndValidate(t.TempDir()); !domain.HasCode(err, domain.ErrorInvalidRequest) {
		t.Fatalf("err=%v", err)
	}
}

func TestWorkspacePathNormalizesSymlinkRootAndRejectsSymlinkEscape(t *testing.T) {
	actualRoot := filepath.Join(t.TempDir(), "actual")
	outside := t.TempDir()
	inside := filepath.Join(actualRoot, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(actualRoot, rootLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	escape := filepath.Join(actualRoot, "escape-link")
	if err := os.Symlink(outside, escape); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	policy, err := New([]string{rootLink})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := policy.NormalizeAndValidate(inside); err != nil || got != filepath.Clean(inside) {
		t.Fatalf("inside got=%s err=%v", got, err)
	}
	if _, err := policy.NormalizeAndValidate(escape); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestWorkspacePathWindowsCaseHandling(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	root := t.TempDir()
	child := filepath.Join(root, "MiXeD")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	policy, err := New([]string{strings.ToUpper(root)})
	if err != nil {
		t.Fatal(err)
	}
	candidate := strings.ReplaceAll(strings.ToLower(child), `\`, `/`)
	if _, err := policy.NormalizeAndValidate(candidate); err != nil {
		t.Fatalf("case-insensitive candidate rejected: %v", err)
	}
}

func TestWorkspacePathRejectsDifferentVolumeWhenAvailable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	root := t.TempDir()
	currentVolume := strings.ToUpper(filepath.VolumeName(root))
	otherVolume := ""
	for drive := 'C'; drive <= 'Z'; drive++ {
		candidate := string(drive) + `:\`
		if strings.EqualFold(candidate[:2], currentVolume) {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			otherVolume = candidate
			break
		}
	}
	if otherVolume == "" {
		t.Skip("no alternate volume available")
	}
	outside := filepath.Join(otherVolume, "opendray-b2f2-volume-test")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Skipf("alternate volume unavailable for writes: %v", err)
	}
	defer os.RemoveAll(outside)
	policy, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.NormalizeAndValidate(outside); err == nil {
		t.Fatalf("different volume accepted: %s", outside)
	}
}
