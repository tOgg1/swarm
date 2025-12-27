package sequences

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

// LoadBuiltinSequences returns the built-in sequences bundled with Swarm.
func LoadBuiltinSequences() ([]*Sequence, error) {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil, fmt.Errorf("read builtin sequences: %w", err)
	}

	sequences := make([]*Sequence, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := "builtin/" + entry.Name()
		data, err := builtinFS.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read builtin sequence %s: %w", entry.Name(), err)
		}
		seq, err := parseSequence(data)
		if err != nil {
			return nil, fmt.Errorf("parse builtin sequence %s: %w", entry.Name(), err)
		}
		seq.Source = "builtin"
		sequences = append(sequences, seq)
	}

	sort.Slice(sequences, func(i, j int) bool {
		return sequences[i].Name < sequences[j].Name
	})

	return sequences, nil
}
