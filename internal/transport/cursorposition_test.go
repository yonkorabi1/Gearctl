package transport

import (
	"bytes"
	"testing"
)

func TestRespondToCursorPositionQueriesRespondsOnMatch(t *testing.T) {
	var written [][]byte
	write := func(b []byte) error {
		written = append(written, append([]byte{}, b...))
		return nil
	}

	tail := respondToCursorPositionQueries(write, nil, []byte("hello \x1b[6n world"))

	if len(written) != 1 {
		t.Fatalf("expected exactly one reply, got %d: %q", len(written), written)
	}
	if !bytes.Equal(written[0], cursorPositionReply) {
		t.Fatalf("reply = %q, want %q", written[0], cursorPositionReply)
	}
	if len(tail) != len(cursorPositionQuery)-1 {
		t.Fatalf("tail length = %d, want %d", len(tail), len(cursorPositionQuery)-1)
	}
}

func TestRespondToCursorPositionQueriesNoQueryIsNoOp(t *testing.T) {
	var written [][]byte
	write := func(b []byte) error {
		written = append(written, b)
		return nil
	}

	respondToCursorPositionQueries(write, nil, []byte("just some ordinary CLI output\r\n"))

	if len(written) != 0 {
		t.Fatalf("expected no replies for plain output, got %d: %q", len(written), written)
	}
}

func TestRespondToCursorPositionQueriesFindsQuerySplitAcrossChunks(t *testing.T) {
	var written [][]byte
	write := func(b []byte) error {
		written = append(written, append([]byte{}, b...))
		return nil
	}

	// Split "\x1b[6n" across two reads, as a real stream boundary could.
	tail := respondToCursorPositionQueries(write, nil, []byte("prefix \x1b[6"))
	respondToCursorPositionQueries(write, tail, []byte("n suffix"))

	if len(written) != 1 {
		t.Fatalf("expected exactly one reply for a split query, got %d: %q", len(written), written)
	}
	if !bytes.Equal(written[0], cursorPositionReply) {
		t.Fatalf("reply = %q, want %q", written[0], cursorPositionReply)
	}
}

func TestRespondToCursorPositionQueriesHandlesMultipleQueriesInOneChunk(t *testing.T) {
	var written [][]byte
	write := func(b []byte) error {
		written = append(written, append([]byte{}, b...))
		return nil
	}

	respondToCursorPositionQueries(write, nil, []byte("\x1b[6n and again \x1b[6n"))

	if len(written) != 2 {
		t.Fatalf("expected two replies, got %d: %q", len(written), written)
	}
}

func TestRespondToCursorPositionQueriesNeverMutatesInputChunk(t *testing.T) {
	chunk := []byte("before \x1b[6n after")
	original := append([]byte{}, chunk...)

	respondToCursorPositionQueries(func([]byte) error { return nil }, nil, chunk)

	if !bytes.Equal(chunk, original) {
		t.Fatalf("chunk was mutated: got %q, want %q", chunk, original)
	}
}
