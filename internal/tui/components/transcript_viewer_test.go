package components

import (
	"reflect"
	"testing"
	"time"
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

func TestTranscriptViewerMaxLinesTrimTimestamps(t *testing.T) {
	viewer := NewTranscriptViewer()
	viewer.SetMaxLines(2)
	now := time.Now()
	lines := []string{"one", "two", "three"}
	timestamps := []time.Time{
		now.Add(-3 * time.Minute),
		now.Add(-2 * time.Minute),
		now.Add(-1 * time.Minute),
	}

	viewer.SetLinesWithTimestamps(lines, timestamps)

	if len(viewer.LineTimestamps) != 2 {
		t.Fatalf("expected 2 timestamps, got %d", len(viewer.LineTimestamps))
	}
	if !viewer.LineTimestamps[0].Equal(timestamps[1]) || !viewer.LineTimestamps[1].Equal(timestamps[2]) {
		t.Fatalf("timestamps not trimmed correctly: %v", viewer.LineTimestamps)
	}
}

func TestTranscriptViewerTimestampMismatchClears(t *testing.T) {
	viewer := NewTranscriptViewer()
	viewer.SetLinesWithTimestamps([]string{"a", "b"}, []time.Time{time.Now()})
	if len(viewer.LineTimestamps) != 0 {
		t.Fatalf("expected timestamps to clear on mismatch, got %d", len(viewer.LineTimestamps))
	}
}
