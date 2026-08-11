import { capturePrompt, waitForPrompt, DataSource } from '../src/transport/prompt-detector';
import { OltCliError } from '../src/types';

/** A fake DataSource with no real I/O -- lets the test script exactly what bytes arrive and when. */
class FakeDataSource implements DataSource {
  private listeners: Array<(chunk: Buffer) => void> = [];
  public written: string[] = [];

  onData(cb: (chunk: Buffer) => void): () => void {
    this.listeners.push(cb);
    return () => {
      const idx = this.listeners.indexOf(cb);
      if (idx !== -1) this.listeners.splice(idx, 1);
    };
  }

  write(data: string): void {
    this.written.push(data);
  }

  /** Test helper: simulate the device sending bytes back. */
  emit(text: string): void {
    const buf = Buffer.from(text, 'utf8');
    for (const cb of this.listeners) cb(buf);
  }
}

describe('capturePrompt', () => {
  it('captures the last non-empty line after the buffer goes quiet', async () => {
    const source = new FakeDataSource();
    const promise = capturePrompt(source, 2000);
    // Simulate the device responding to the bare newline we sent.
    setTimeout(() => source.emit('\r\nWelcome to MockOLT\r\nmock-olt> '), 10);
    const prompt = await promise;
    expect(prompt).toBe('mock-olt>');
    expect(source.written).toContain('\n');
  });

  it('throws when the device produces no output at all', async () => {
    const source = new FakeDataSource();
    await expect(capturePrompt(source, 500)).rejects.toThrow(OltCliError);
  });
});

describe('waitForPrompt', () => {
  it('strips the echoed command and trailing prompt, returning just the output', async () => {
    const source = new FakeDataSource();
    const promise = waitForPrompt(source, 'display version', 'mock-olt>', 2000);
    setTimeout(
      () => source.emit('display version\r\nMockOLT Software Version 1.0.0\r\nmock-olt> '),
      10,
    );
    const output = await promise;
    expect(output).toBe('MockOLT Software Version 1.0.0');
  });

  it('rejects with a timeout OltCliError when the prompt never reappears', async () => {
    const source = new FakeDataSource();
    const promise = waitForPrompt(source, 'hang-forever', 'mock-olt>', 200);
    await expect(promise).rejects.toMatchObject({ type: 'timeout' });
  });
});
