package workspacepolicy

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

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
