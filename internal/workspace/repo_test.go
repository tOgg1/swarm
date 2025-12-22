package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectRepoRootFromPathsSingle(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("create .git dir: %v", err)
	}
	subdir := filepath.Join(repo, "src")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("create subdir: %v", err)
	}

	root, err := detectRepoRootFromPaths([]string{subdir})
	if err != nil {
		t.Fatalf("detectRepoRootFromPaths returned error: %v", err)
	}
	if root != normalizePath(repo) {
		t.Fatalf("expected root %q, got %q", normalizePath(repo), root)
	}
}

func TestDetectRepoRootFromPathsNested(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("create outer .git dir: %v", err)
	}
	nested := filepath.Join(repo, "nested")
	if err := os.MkdirAll(filepath.Join(nested, ".git"), 0o755); err != nil {
		t.Fatalf("create nested .git dir: %v", err)
	}
	outerPath := filepath.Join(repo, "service")
	nestedPath := filepath.Join(nested, "service")
	if err := os.MkdirAll(outerPath, 0o755); err != nil {
		t.Fatalf("create outer path: %v", err)
	}
	if err := os.MkdirAll(nestedPath, 0o755); err != nil {
		t.Fatalf("create nested path: %v", err)
	}

	root, err := detectRepoRootFromPaths([]string{outerPath, nestedPath})
	if err != nil {
		t.Fatalf("detectRepoRootFromPaths returned error: %v", err)
	}
	if root != normalizePath(repo) {
		t.Fatalf("expected root %q, got %q", normalizePath(repo), root)
	}
}

func TestDetectRepoRootFromPathsAmbiguous(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoA, ".git"), 0o755); err != nil {
		t.Fatalf("create repoA .git dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoB, ".git"), 0o755); err != nil {
		t.Fatalf("create repoB .git dir: %v", err)
	}
	pathA := filepath.Join(repoA, "a")
	pathB := filepath.Join(repoB, "b")
	if err := os.MkdirAll(pathA, 0o755); err != nil {
		t.Fatalf("create pathA: %v", err)
	}
	if err := os.MkdirAll(pathB, 0o755); err != nil {
		t.Fatalf("create pathB: %v", err)
	}

	_, err := detectRepoRootFromPaths([]string{pathA, pathB})
	if err == nil {
		t.Fatal("expected ambiguous error")
	}
	var amb *AmbiguousRepoRootError
	if !errors.As(err, &amb) {
		t.Fatalf("expected AmbiguousRepoRootError, got %T", err)
	}
	if len(amb.Roots) != 2 {
		t.Fatalf("expected 2 roots, got %d", len(amb.Roots))
	}
	rootMap := map[string]struct{}{}
	for _, root := range amb.Roots {
		rootMap[root] = struct{}{}
	}
	if _, ok := rootMap[normalizePath(repoA)]; !ok {
		t.Fatalf("missing repoA root %q", normalizePath(repoA))
	}
	if _, ok := rootMap[normalizePath(repoB)]; !ok {
		t.Fatalf("missing repoB root %q", normalizePath(repoB))
	}
}

func TestDetectRepoRootFromPathsNoGit(t *testing.T) {
	base := t.TempDir()
	pathA := filepath.Join(base, "app", "a")
	pathB := filepath.Join(base, "app", "b")
	if err := os.MkdirAll(pathA, 0o755); err != nil {
		t.Fatalf("create pathA: %v", err)
	}
	if err := os.MkdirAll(pathB, 0o755); err != nil {
		t.Fatalf("create pathB: %v", err)
	}

	root, err := detectRepoRootFromPaths([]string{pathA, pathB})
	if err != nil {
		t.Fatalf("detectRepoRootFromPaths returned error: %v", err)
	}
	expected := normalizePath(filepath.Join(base, "app"))
	if root != expected {
		t.Fatalf("expected root %q, got %q", expected, root)
	}
}
