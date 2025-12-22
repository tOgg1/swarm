// Package workspace provides helpers for workspace lifecycle management.
package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/opencode-ai/swarm/internal/models"
)

// ValidateRepoPath checks that a repository path exists and is a directory.
func ValidateRepoPath(repoPath string) error {
	if repoPath == "" {
		return models.ErrInvalidRepoPath
	}

	info, err := os.Stat(repoPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("repository path does not exist: %s", repoPath)
		}
		return fmt.Errorf("failed to stat repository path %s: %w", repoPath, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("repository path is not a directory: %s", repoPath)
	}

	return nil
}

// DetectGitInfo inspects a repo path and returns git metadata if available.
func DetectGitInfo(repoPath string) (*models.GitInfo, error) {
	if err := ValidateRepoPath(repoPath); err != nil {
		return nil, err
	}

	isRepo, err := isGitRepo(repoPath)
	if err != nil {
		return nil, err
	}

	info := &models.GitInfo{IsRepo: isRepo}
	if !isRepo {
		return info, nil
	}

	if branch, err := runGitTrim(repoPath, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		info.Branch = branch
	}

	if lastCommit, err := runGitTrim(repoPath, "rev-parse", "HEAD"); err == nil {
		info.LastCommit = lastCommit
	}

	if remote, err := runGitTrim(repoPath, "config", "--get", "remote.origin.url"); err == nil {
		info.RemoteURL = remote
	}

	if status, err := runGitTrim(repoPath, "status", "--porcelain"); err == nil {
		info.IsDirty = status != ""
	}

	if counts, err := runGitTrim(repoPath, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"); err == nil {
		parts := strings.Fields(counts)
		if len(parts) == 2 {
			if ahead, err := strconv.Atoi(parts[0]); err == nil {
				info.Ahead = ahead
			}
			if behind, err := strconv.Atoi(parts[1]); err == nil {
				info.Behind = behind
			}
		}
	}

	return info, nil
}

func isGitRepo(repoPath string) (bool, error) {
	out, _, err := runGit(repoPath, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return false, err
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, err
	}

	return strings.TrimSpace(out) == "true", nil
}

func runGitTrim(repoPath string, args ...string) (string, error) {
	stdout, _, err := runGit(repoPath, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout), nil
}

func runGit(repoPath string, args ...string) (string, string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// AmbiguousRepoRootError indicates multiple repo roots were detected.
type AmbiguousRepoRootError struct {
	Roots []string
}

func (e *AmbiguousRepoRootError) Error() string {
	if len(e.Roots) == 0 {
		return "ambiguous repository roots"
	}
	return fmt.Sprintf("ambiguous repository roots: %s", strings.Join(e.Roots, ", "))
}

// detectRepoRootFromPaths finds a common repo root for a set of pane paths.
func detectRepoRootFromPaths(paths []string) (string, error) {
	cleaned := uniquePaths(paths)
	if len(cleaned) == 0 {
		return "", fmt.Errorf("no pane paths provided")
	}

	var roots []string
	for _, path := range cleaned {
		if root, ok := findGitRoot(path); ok {
			roots = append(roots, root)
		}
	}

	roots = uniquePaths(roots)
	switch len(roots) {
	case 0:
		common := commonAncestor(cleaned)
		if common != "" {
			return common, nil
		}
		return cleaned[0], nil
	case 1:
		return roots[0], nil
	default:
		common := commonAncestor(roots)
		if common != "" && isGitRoot(common) {
			return common, nil
		}
		return "", &AmbiguousRepoRootError{Roots: roots}
	}
}

func findGitRoot(path string) (string, bool) {
	current := normalizePath(path)
	if current == "" {
		return "", false
	}
	for {
		if isGitRoot(current) {
			return current, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", false
}

func isGitRoot(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(path, ".git"))
	if err != nil {
		return false
	}
	return info.IsDir() || info.Mode().IsRegular()
}

func normalizePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return cleaned
	}
	return abs
}

func commonAncestor(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	common := normalizePath(paths[0])
	if common == "" {
		return ""
	}
	for _, path := range paths[1:] {
		candidate := normalizePath(path)
		if candidate == "" {
			continue
		}
		for !isPathAncestor(common, candidate) {
			parent := filepath.Dir(common)
			if parent == common {
				return ""
			}
			common = parent
		}
	}
	return common
}

func isPathAncestor(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	separator := string(filepath.Separator)
	return rel != ".." && !strings.HasPrefix(rel, ".."+separator)
}

func uniquePaths(paths []string) []string {
	seen := make(map[string]struct{})
	unique := make([]string, 0, len(paths))
	for _, path := range paths {
		cleaned := normalizePath(path)
		if cleaned == "" {
			continue
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		unique = append(unique, cleaned)
	}
	sort.Strings(unique)
	return unique
}
