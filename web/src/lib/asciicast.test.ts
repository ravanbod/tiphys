import { describe, expect, it } from 'vitest';
import { deriveCommands, findOutputMatches, parseAsciicast, stripAnsi } from './asciicast';

const cast = [
  '{"version":2,"width":80,"height":24,"timestamp":1}',
  '[0.1,"o","$ "]',
  '[1.0,"i","df -h"]',
  '[1.2,"i","\\r"]',
  '[1.3,"o","Filesystem  Size\\r\\n/dev/nvme1n1 200G\\r\\n"]',
  '[3.0,"i","ls shardz\\u007f/\\r"]',
  '[3.5,"o","segments_12 write.\\u001b[1mlock\\u001b[0m\\r\\n"]',
  '[5.0,"i","\\u001b[A"]',
  '[6.0,"i","exit\\r"]',
  'not json',
].join('\n');

describe('parseAsciicast', () => {
  it('parses header and events, skipping garbage', () => {
    const p = parseAsciicast(cast);
    expect(p.header?.width).toBe(80);
    expect(p.events).toHaveLength(8);
    expect(p.duration).toBe(6);
  });
  it('tolerates an empty file', () => {
    expect(parseAsciicast('')).toEqual({ header: null, events: [], duration: 0 });
  });
});

describe('deriveCommands', () => {
  it('splits at Enter, handles backspace and arrows', () => {
    const cmds = deriveCommands(parseAsciicast(cast).events);
    expect(cmds).toEqual([
      { offsetSeconds: 1.0, text: 'df -h' },
      { offsetSeconds: 3.0, text: 'ls shard/' },
      { offsetSeconds: 6.0, text: 'exit' },
    ]);
  });
  it('ctrl-c discards the line', () => {
    expect(deriveCommands([{ t: 1, code: 'i', data: 'rm -rf\x03ls\r' }])).toEqual([{ offsetSeconds: 1, text: 'ls' }]);
  });
});

describe('findOutputMatches', () => {
  it('finds matches ignoring ANSI and case', () => {
    expect(stripAnsi('a\x1b[1mb\x1b[0m')).toBe('ab');
    expect(findOutputMatches(parseAsciicast(cast).events, 'WRITE.LOCK')).toEqual([3.5]);
  });
  it('finds matches that span events', () => {
    const ev = [{ t: 1, code: 'o', data: 'wri' }, { t: 2, code: 'o', data: 'te.lock' }];
    expect(findOutputMatches(ev, 'write')).toEqual([1]);
  });
  it('empty query', () => {
    expect(findOutputMatches([], '')).toEqual([]);
  });
});
