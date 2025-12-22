package beads

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// BeadsDirName is the directory where beads stores its data.
	BeadsDirName = ".beads"
	// IssuesFileName is the JSONL file containing issue records.
	IssuesFileName = "issues.jsonl"
)

// IssuesPath returns the default issues.jsonl location for a repo path.
func IssuesPath(repoPath string) string {
	return filepath.Join(repoPath, BeadsDirName, IssuesFileName)
}

// LoadIssues reads and parses a beads issues.jsonl file.
func LoadIssues(path string) ([]Issue, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return ParseIssues(file)
}

// ParseIssues reads JSONL issue records from a reader.
func ParseIssues(r io.Reader) ([]Issue, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	issues := []Issue{}
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var issue Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return nil, fmt.Errorf("parse beads issue line %d: %w", lineNo, err)
		}
		issues = append(issues, issue)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan beads issues: %w", err)
	}

	return issues, nil
}

// Summaries maps issues into display-friendly summaries.
func Summaries(issues []Issue) []TaskSummary {
	summaries := make([]TaskSummary, 0, len(issues))
	for _, issue := range issues {
		summaries = append(summaries, issue.Summary())
	}
	return summaries
}
