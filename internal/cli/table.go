// Package cli provides table helpers for human-readable output.
package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

const tablePadding = 2

func writeTable(out io.Writer, headers []string, rows [][]string) error {
	writer := tabwriter.NewWriter(out, 0, 0, tablePadding, ' ', tabwriter.StripEscape)
	if len(headers) > 0 {
		fmt.Fprintln(writer, strings.Join(headers, "\t"))
	}
	for _, row := range rows {
		fmt.Fprintln(writer, strings.Join(row, "\t"))
	}
	return writer.Flush()
}

func formatYesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
