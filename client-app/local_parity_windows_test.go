//go:build windows

package main

import (
	"path/filepath"
	"testing"
)

// A transfer streamed to a local connection: the local disk's 100 MB per
// file does not apply (walkLarge, openReadLarge, writeBegin large); the
// explorer's own path keeps it.
func TestLocalFSLargeTransfers(t *testing.T) {
	l, _, root := testLocalFS(t)
	if _, err := l.writeBegin(root, "big.iso", fsWriteOpts{Size: localMaxFileBytes + 1}); codeOf(err) != "too_large" {
		t.Fatalf("explorer path: %v", err)
	}
	r, err := l.writeBegin(root, "big.iso", fsWriteOpts{Size: localMaxFileBytes + 1, Large: true})
	if err != nil || r == nil || r.Handle == "" {
		t.Fatalf("large write: %v", err)
	}
	l.writeAbort(r.Handle)
	if _, err := l.writeBegin(root, "huge.iso", fsWriteOpts{Size: localMaxStreamBytes + 1, Large: true}); codeOf(err) != "too_large" {
		t.Fatalf("over the stream bound: %v", err)
	}
	// The walk: the same files, a large limit.
	mustWrite(t, filepath.Join(root, "a.txt"), "abc")
	w, err := l.walkLarge(root, []string{"a.txt"})
	if err != nil || len(w.Items) != 1 || w.Skipped.TooLarge != 0 {
		t.Fatalf("walkLarge: %+v %v", w, err)
	}
	o, err := l.openReadLarge(filepath.Join(root, "a.txt"))
	if err != nil || o.Size != 3 {
		t.Fatalf("openReadLarge: %+v %v", o, err)
	}
	l.closeRead(o.Handle)
}
