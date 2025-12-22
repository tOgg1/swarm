package components

import (
	"strings"
	"testing"

	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/tui/styles"
)

func TestRenderGitStatusCompact(t *testing.T) {
	s := styles.DefaultStyles()

	tests := []struct {
		name     string
		info     *models.GitInfo
		contains []string
	}{
		{
			name:     "nil info",
			info:     nil,
			contains: []string{"--"},
		},
		{
			name:     "not a repo",
			info:     &models.GitInfo{IsRepo: false},
			contains: []string{"--"},
		},
		{
			name:     "clean branch",
			info:     &models.GitInfo{IsRepo: true, Branch: "main"},
			contains: []string{"main"},
		},
		{
			name:     "dirty branch",
			info:     &models.GitInfo{IsRepo: true, Branch: "main", IsDirty: true},
			contains: []string{"main", "*"},
		},
		{
			name:     "ahead of remote",
			info:     &models.GitInfo{IsRepo: true, Branch: "main", Ahead: 2},
			contains: []string{"main", "↑2"},
		},
		{
			name:     "behind remote",
			info:     &models.GitInfo{IsRepo: true, Branch: "main", Behind: 3},
			contains: []string{"main", "↓3"},
		},
		{
			name:     "ahead and behind",
			info:     &models.GitInfo{IsRepo: true, Branch: "feature", Ahead: 1, Behind: 2, IsDirty: true},
			contains: []string{"feature", "↑1", "↓2", "*"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RenderGitStatusCompact(s, tt.info)
			for _, want := range tt.contains {
				if !strings.Contains(result, want) {
					t.Errorf("RenderGitStatusCompact() = %q, want to contain %q", result, want)
				}
			}
		})
	}
}

func TestRenderGitSyncStatus(t *testing.T) {
	s := styles.DefaultStyles()

	tests := []struct {
		name     string
		info     *models.GitInfo
		contains string
	}{
		{
			name:     "nil info",
			info:     nil,
			contains: "",
		},
		{
			name:     "synced",
			info:     &models.GitInfo{IsRepo: true, Ahead: 0, Behind: 0},
			contains: "synced",
		},
		{
			name:     "ahead",
			info:     &models.GitInfo{IsRepo: true, Ahead: 5},
			contains: "ahead",
		},
		{
			name:     "behind",
			info:     &models.GitInfo{IsRepo: true, Behind: 3},
			contains: "behind",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RenderGitSyncStatus(s, tt.info)
			if tt.contains != "" && !strings.Contains(result, tt.contains) {
				t.Errorf("RenderGitSyncStatus() = %q, want to contain %q", result, tt.contains)
			}
		})
	}
}

func TestRenderGitChangeSummary(t *testing.T) {
	s := styles.DefaultStyles()

	tests := []struct {
		name     string
		data     GitStatusData
		contains []string
	}{
		{
			name:     "no changes",
			data:     GitStatusData{Info: &models.GitInfo{IsRepo: true}},
			contains: []string{"No changes"},
		},
		{
			name: "staged files",
			data: GitStatusData{
				Info:        &models.GitInfo{IsRepo: true, IsDirty: true},
				StagedFiles: []string{"file1.go", "file2.go"},
			},
			contains: []string{"2 staged"},
		},
		{
			name: "modified files",
			data: GitStatusData{
				Info:         &models.GitInfo{IsRepo: true, IsDirty: true},
				ChangedFiles: []string{"a.go", "b.go", "c.go"},
			},
			contains: []string{"3 modified"},
		},
		{
			name: "untracked files",
			data: GitStatusData{
				Info:           &models.GitInfo{IsRepo: true, IsDirty: true},
				UntrackedFiles: []string{"new.go"},
			},
			contains: []string{"1 untracked"},
		},
		{
			name: "mixed changes",
			data: GitStatusData{
				Info:           &models.GitInfo{IsRepo: true, IsDirty: true},
				StagedFiles:    []string{"staged.go"},
				ChangedFiles:   []string{"mod1.go", "mod2.go"},
				UntrackedFiles: []string{"new1.go", "new2.go", "new3.go"},
			},
			contains: []string{"1 staged", "2 modified", "3 untracked"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RenderGitChangeSummary(s, tt.data)
			for _, want := range tt.contains {
				if !strings.Contains(result, want) {
					t.Errorf("RenderGitChangeSummary() = %q, want to contain %q", result, want)
				}
			}
		})
	}
}

func TestRenderGitBranchBadge(t *testing.T) {
	s := styles.DefaultStyles()

	tests := []struct {
		name     string
		info     *models.GitInfo
		contains string
	}{
		{
			name:     "nil info",
			info:     nil,
			contains: "",
		},
		{
			name:     "with branch",
			info:     &models.GitInfo{IsRepo: true, Branch: "develop"},
			contains: "develop",
		},
		{
			name:     "dirty branch",
			info:     &models.GitInfo{IsRepo: true, Branch: "main", IsDirty: true},
			contains: "*",
		},
		{
			name:     "long branch name truncated",
			info:     &models.GitInfo{IsRepo: true, Branch: "feature/very-long-branch-name-that-should-be-truncated"},
			contains: "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RenderGitBranchBadge(s, tt.info)
			if tt.contains != "" && !strings.Contains(result, tt.contains) {
				t.Errorf("RenderGitBranchBadge() = %q, want to contain %q", result, tt.contains)
			}
		})
	}
}

func TestExtractRemoteName(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"git@github.com:owner/repo.git", "owner/repo"},
		{"https://github.com/owner/repo.git", "owner/repo"},
		{"git@github.com:owner/repo", "owner/repo"},
		{"https://github.com/owner/repo", "owner/repo"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := extractRemoteName(tt.url)
			if got != tt.want {
				t.Errorf("extractRemoteName(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestTruncateFilePath(t *testing.T) {
	tests := []struct {
		path   string
		maxLen int
		want   string
	}{
		{"short.go", 20, "short.go"},
		{"very/long/path/to/file.go", 15, "...th/to/file.go"},
		{"a.go", 3, "..."},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := truncateFilePath(tt.path, tt.maxLen)
			if len(got) > tt.maxLen && tt.maxLen > 5 {
				t.Errorf("truncateFilePath(%q, %d) = %q (len %d), exceeds maxLen", tt.path, tt.maxLen, got, len(got))
			}
		})
	}
}
