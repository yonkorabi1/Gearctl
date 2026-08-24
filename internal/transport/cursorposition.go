package transport

import "bytes"

// cursorPositionQuery is the VT100/ANSI "Device Status Report" escape
// sequence (ESC [ 6 n) a real terminal answers with its current cursor
// row/column (ESC [ <row> ; <col> R) -- confirmed live against a real
// MikroTik RouterOS 6.49.19 CHR: its SSH console sends this the moment a
// PTY is attached (this codebase requests "vt100" specifically, see
// RequestPty in OpenSSHSession, which is exactly the terminal type this
// query is part of emulating correctly) and, getting no reply, redraws/
// re-echoes the input line over and over waiting for one -- a real hang
// hazard for prompt detection: waitForPrompt's substring-match path
// matches the FIRST occurrence of the session's own prompt text after a
// command's echo, and every one of those repeated redraws starts with
// exactly that prompt text, so completion was detected on an early
// redraw, well before the device's real answer (sent only after several
// more redraws) ever arrived -- observed live truncating "/system
// identity print"'s entire "name: ..." line to nothing.
//
// Answering this is universally correct terminal-emulation behavior, not
// a MikroTik-specific hack: any device that queries cursor position over
// a PTY we claimed was "vt100" is owed a real answer per the terminal
// type we asked for. Devices that never send the query (every other
// vendor driver tested against so far) are completely unaffected -- see
// TestRespondToCursorPositionQueriesNoQueryIsNoOp.
var cursorPositionQuery = []byte("\x1b[6n")

// cursorPositionReply is the canned answer sent back -- row/column values
// are arbitrary (RouterOS's console only needs A reply to stop retrying,
// not an accurate one; confirmed live that any well-formed CPR response
// satisfies it) but kept within the PTY size this codebase actually
// requests (40 rows x 80 columns, see OpenSSHSession) rather than an
// out-of-bounds placeholder, in case some other device someday validates
// the reported position against its own idea of the terminal size.
var cursorPositionReply = []byte("\x1b[24;80R")

// respondToCursorPositionQueries scans tail+chunk for cursorPositionQuery
// and, for each occurrence found, writes cursorPositionReply via write
// immediately (fire-and-forget: a write failure here means the
// connection is already dead and the caller's own next read will surface
// that). chunk is never modified -- callers still forward the original
// bytes downstream unchanged; this only ever adds an outgoing reply, the
// same way a real terminal emulator would, without altering what the
// caller sees.
//
// tail carries forward the last few bytes of the PREVIOUS chunk (pass nil
// for the first call on a fresh reader loop) so a query split across two
// stdout reads is still found -- SSH delivers a stream, not
// message-framed packets, so a 4-byte escape sequence landing across a
// read boundary is a real possibility, not just defensive theater.
// Returns the new tail to carry into the next call.
func respondToCursorPositionQueries(write func([]byte) error, tail, chunk []byte) []byte {
	combined := append(append([]byte{}, tail...), chunk...)

	searchFrom := 0
	for {
		idx := bytes.Index(combined[searchFrom:], cursorPositionQuery)
		if idx == -1 {
			break
		}
		_ = write(cursorPositionReply)
		searchFrom += idx + len(cursorPositionQuery)
	}

	// Carry forward only enough trailing bytes to catch a query split
	// across the NEXT boundary -- one byte short of the full query length
	// is the most that could possibly be a partial match.
	keep := len(cursorPositionQuery) - 1
	if keep > len(combined) {
		keep = len(combined)
	}
	return combined[len(combined)-keep:]
}
