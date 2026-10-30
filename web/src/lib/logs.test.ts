import { describe, expect, it } from 'vitest';
import { LineSplitter, countMatches, detectLevel, parseLogLine, shortTs, splitHighlight, splitTimestamp } from './logs';

describe('timestamps', () => {
  it('splits an RFC3339 prefix', () => {
    const s = splitTimestamp('2026-10-20T13:41:04.771Z ERROR psp timeout');
    expect(s.ts).toBe('2026-10-20T13:41:04.771Z');
    expect(s.rest).toBe('ERROR psp timeout');
    expect(shortTs(s.ts)).toBe('13:41:04.771');
  });
  it('leaves lines without a timestamp alone', () => {
    expect(splitTimestamp('hello')).toEqual({ ts: '', rest: 'hello' });
  });
});

describe('levels', () => {
  it('detects leading words', () => {
    expect(detectLevel('WARN gateway slow').level).toBe('W');
    expect(detectLevel('[error] boom').level).toBe('E');
    expect(detectLevel('info started').rest).toBe('started');
  });
  it('detects key=value and JSON', () => {
    expect(detectLevel('ts=1 level=debug msg=x').level).toBe('D');
    expect(detectLevel('{"level":"error","msg":"x"}').level).toBe('E');
  });
  it('no level', () => {
    expect(detectLevel('GET /healthz 200').level).toBe('');
  });
});

describe('parseLogLine', () => {
  it('parses timestamp + level + message', () => {
    const l = parseLogLine('2026-10-20T13:41:04.771Z ERROR psp timeout\r', true);
    expect(l.ts).toBe('2026-10-20T13:41:04.771Z');
    expect(l.level).toBe('E');
    expect(l.msg).toBe('psp timeout');
    expect(l.raw.endsWith('\r')).toBe(false);
  });
  it('ignores timestamps when not requested', () => {
    expect(parseLogLine('2026-10-20T13:41:04Z hi', false).ts).toBe('');
  });
});

describe('LineSplitter', () => {
  it('keeps partial lines across chunks', () => {
    const s = new LineSplitter();
    expect(s.push('a\nb')).toEqual(['a']);
    expect(s.push('c\nd\n')).toEqual(['bc', 'd']);
    expect(s.push('tail')).toEqual([]);
    expect(s.flush()).toEqual(['tail']);
    expect(s.flush()).toEqual([]);
  });
});

describe('search', () => {
  it('highlights case-insensitively and escapes regex chars', () => {
    expect(splitHighlight('a Timeout (x)', 'timeout')).toEqual([
      { text: 'a ', hit: false }, { text: 'Timeout', hit: true }, { text: ' (x)', hit: false },
    ]);
    expect(splitHighlight('f(x)', '(x)')[1]).toEqual({ text: '(x)', hit: true });
    expect(splitHighlight('abc', '')).toEqual([{ text: 'abc', hit: false }]);
  });
  it('counts matching lines', () => {
    expect(countMatches([{ raw: 'a' }, { raw: 'TIMEOUT' }, { raw: 'b timeout' }], 'timeout')).toEqual([1, 2]);
  });
});
