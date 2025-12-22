package components

import (
	"reflect"
	"testing"
)

func TestTranscriptViewerMaxLinesTrim(t *testing.T) {
	viewer := NewTranscriptViewer()
	viewer.SetMaxLines(3)
	viewer.SetLines([]string{"one", "two", "three", "four"})

	want := []string{"two", "three", "four"}
	if !reflect.DeepEqual(viewer.Lines, want) {
		t.Fatalf("expected lines %v, got %v", want, viewer.Lines)
	}
}

func TestTranscriptViewerMaxLinesAdjustsScroll(t *testing.T) {
	viewer := NewTranscriptViewer()
	viewer.SetMaxLines(3)
	viewer.Height = 3
	viewer.ScrollOffset = 3
	viewer.SetLines([]string{"a", "b", "c", "d", "e"})

	if viewer.ScrollOffset != 1 {
		t.Fatalf("expected ScrollOffset 1, got %d", viewer.ScrollOffset)
	}
}
