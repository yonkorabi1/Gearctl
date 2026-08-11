// ============================================================================
// Vendor-agnostic prompt detection for interactive CLI shells.
//
// The core problem this solves: an OLT CLI session is a stateful, ongoing
// shell -- there's no "command exits, socket closes" signal like a one-shot
// SSH exec channel. We need to know when a command has finished producing
// output WITHOUT knowing anything about the vendor's actual prompt syntax
// ("<HUAWEI>", "OLT#", "olt>", etc. all vary).
//
// The approach (see README.md "Prompt-return detection"):
//   1. On connect, before running any real command, send a bare newline and
//      wait for the buffer to go quiet (no new bytes for `quiescenceMs`).
//      Whatever trailing line is left is treated as the session's prompt.
//   2. For every subsequent command, accumulate output and check whether
//      the buffer's tail matches that captured prompt. When it does, the
//      command is done.
//
// This needs zero vendor knowledge and works the same for SSH and Telnet,
// since both transports hand this module a minimal `DataSource` (anything
// with .onData()/.write()) rather than their own stream types directly.
// ============================================================================

import { OltCliError } from '../types';

export interface DataSource {
  /** Returns an unsubscribe function -- callers MUST call it once done listening (see below) to avoid leaking listeners for the life of a session. */
  onData(cb: (chunk: Buffer) => void): () => void;
  write(data: string): void;
}

const DEFAULT_QUIESCENCE_MS = 300;

/**
 * Accumulate bytes from `source` until no new data has arrived for
 * `quiescenceMs`, or `maxWaitMs` elapses (safety cap for a device that
 * never goes quiet -- e.g. a broken/chatty CLI). Returns everything
 * accumulated.
 */
export function waitForQuiescence(
  source: DataSource,
  opts: { quiescenceMs?: number; maxWaitMs: number },
): Promise<string> {
  const quiescenceMs = opts.quiescenceMs ?? DEFAULT_QUIESCENCE_MS;
  return new Promise((resolve) => {
    let buffer = '';
    let quietTimer: NodeJS.Timeout;
    let unsubscribe: () => void;

    const settle = () => {
      clearTimeout(quietTimer);
      clearTimeout(maxTimer);
      unsubscribe();
      resolve(buffer);
    };

    const maxTimer = setTimeout(settle, opts.maxWaitMs);

    const resetQuietTimer = () => {
      clearTimeout(quietTimer);
      quietTimer = setTimeout(settle, quiescenceMs);
    };

    unsubscribe = source.onData((chunk) => {
      buffer += chunk.toString('utf8');
      resetQuietTimer();
    });
    resetQuietTimer();
  });
}

/**
 * Capture the shell's idle prompt: send a bare newline, wait for
 * quiescence, and treat the last non-empty line of whatever came back as
 * the prompt. Trimmed of surrounding whitespace only -- deliberately NOT
 * regex-escaped/interpreted here, matching is a plain substring check in
 * waitForPrompt() below.
 */
export async function capturePrompt(source: DataSource, maxWaitMs: number): Promise<string> {
  source.write('\n');
  const settled = await waitForQuiescence(source, { maxWaitMs });
  const lines = settled.split(/\r?\n/).map((l) => l.trim()).filter((l) => l.length > 0);
  const prompt = lines[lines.length - 1] ?? '';
  if (!prompt) {
    throw new OltCliError(
      'protocol',
      'Could not capture a shell prompt -- the device produced no output after connecting',
    );
  }
  return prompt;
}

/**
 * Send `command`, then accumulate output until the buffer's tail contains
 * `promptText` (the previously captured prompt) or `timeoutMs` elapses.
 * On success, returns the output with the leading echoed command line and
 * the trailing prompt occurrence stripped. On timeout, rejects with an
 * OltCliError('timeout', ...) -- the caller MUST treat the session as
 * unusable afterward (see ShellSession.sendAndWaitForPrompt's contract).
 */
export function waitForPrompt(
  source: DataSource,
  command: string,
  promptText: string,
  timeoutMs: number,
): Promise<string> {
  return new Promise((resolve, reject) => {
    let buffer = '';
    let settled = false;
    let unsubscribe: () => void;

    const timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      unsubscribe();
      reject(new OltCliError('timeout', `Timed out waiting for prompt after "${command}"`));
    }, timeoutMs);

    unsubscribe = source.onData((chunk) => {
      if (settled) return;
      buffer += chunk.toString('utf8');
      if (buffer.includes(promptText)) {
        settled = true;
        clearTimeout(timer);
        unsubscribe();
        resolve(stripEchoAndPrompt(buffer, command, promptText));
      }
    });

    source.write(command + '\n');
  });
}

/**
 * The device echoes the command back as the first line (standard for both
 * SSH and Telnet interactive shells), and the response ends with the
 * prompt reappearing. Strip both so callers get just the command's real
 * output.
 */
function stripEchoAndPrompt(raw: string, command: string, promptText: string): string {
  let text = raw;
  // Strip the echoed command (first occurrence, allowing for a leading
  // newline/carriage-return already in the buffer from the previous
  // prompt).
  const echoIdx = text.indexOf(command);
  if (echoIdx !== -1) {
    text = text.slice(echoIdx + command.length);
  }
  // Strip the trailing prompt (last occurrence, since output could
  // legitimately contain the prompt substring earlier -- unlikely but
  // not impossible for a generic driver with no vendor knowledge).
  const promptIdx = text.lastIndexOf(promptText);
  if (promptIdx !== -1) {
    text = text.slice(0, promptIdx);
  }
  return text.replace(/^\r?\n/, '').replace(/\r?\n$/, '');
}
