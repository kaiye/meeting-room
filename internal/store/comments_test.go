package store

import "testing"

func TestCommentRevisionGroupIDIsExposed(t *testing.T) {
	comment := Comment{RevisionGroupID: "batch-a"}
	if comment.RevisionGroupID != "batch-a" {
		t.Fatalf("revision group ID = %q, want batch-a", comment.RevisionGroupID)
	}
}
