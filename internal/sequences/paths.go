package sequences

import (
	"os"
	"path/filepath"
)

// SequenceSearchPaths returns sequence search directories in precedence order.
func SequenceSearchPaths(projectDir string) []string {
	paths := make([]string, 0, 3)
	if projectDir != "" {
		paths = append(paths, filepath.Join(projectDir, ".swarm", "sequences"))
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".config", "swarm", "sequences"))
	}

	paths = append(paths, filepath.Join(string(filepath.Separator), "usr", "share", "swarm", "sequences"))
	return paths
}

// LoadSequencesFromSearchPaths loads sequences from search paths with first-hit precedence.
func LoadSequencesFromSearchPaths(projectDir string) ([]*Sequence, error) {
	paths := SequenceSearchPaths(projectDir)
	seen := make(map[string]*Sequence)
	order := make([]string, 0)

	for _, path := range paths {
		sequences, err := LoadSequencesFromDir(path)
		if err != nil {
			return nil, err
		}
		for _, seq := range sequences {
			if _, exists := seen[seq.Name]; exists {
				continue
			}
			seen[seq.Name] = seq
			order = append(order, seq.Name)
		}
	}

	builtins, err := LoadBuiltinSequences()
	if err != nil {
		return nil, err
	}
	for _, seq := range builtins {
		if _, exists := seen[seq.Name]; exists {
			continue
		}
		seen[seq.Name] = seq
		order = append(order, seq.Name)
	}

	resolved := make([]*Sequence, 0, len(order))
	for _, name := range order {
		resolved = append(resolved, seen[name])
	}

	return resolved, nil
}
