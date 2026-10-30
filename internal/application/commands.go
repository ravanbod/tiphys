package application

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"time"
)

// CommandEntry is one command line typed in an exec session.
type CommandEntry struct {
	At            time.Time
	OffsetSeconds float64
	Text          string
}

// maxCommandScanBytes bounds how much of a recording is scanned.
const maxCommandScanBytes = 256 << 20

// ExtractCommands derives the commands typed in a session from the input
// events of its asciicast recording, split at Enter. It is best-effort: tab
// completion, history and cursor keys make the result imperfect, so the UI
// labels it as such and the replay is authoritative.
func ExtractCommands(r io.Reader, started time.Time) ([]CommandEntry, error) {
	sc := bufio.NewReaderSize(io.LimitReader(r, maxCommandScanBytes), 1<<20)
	var out []CommandEntry
	var line []rune
	var lineStart float64
	first := true
	inEscape := 0 // 0 none, 1 after ESC, 2 inside CSI

	for {
		raw, err := sc.ReadBytes('\n')
		if len(raw) > 0 {
			if first {
				first = false // header
			} else {
				var ev []json.RawMessage
				if json.Unmarshal(raw, &ev) == nil && len(ev) == 3 {
					var t float64
					var kind, data string
					if json.Unmarshal(ev[0], &t) == nil && json.Unmarshal(ev[1], &kind) == nil && kind == "i" && json.Unmarshal(ev[2], &data) == nil {
						for _, c := range data {
							switch inEscape {
							case 1:
								if c == '[' || c == 'O' {
									inEscape = 2
								} else {
									inEscape = 0
								}
								continue
							case 2:
								if c >= 0x40 && c <= 0x7e {
									inEscape = 0
								}
								continue
							}
							switch {
							case c == 0x1b:
								inEscape = 1
							case c == '\r' || c == '\n':
								if text := strings.TrimSpace(string(line)); text != "" {
									out = append(out, CommandEntry{At: started.Add(time.Duration(lineStart * float64(time.Second))), OffsetSeconds: lineStart, Text: text})
								}
								line = line[:0]
							case c == 0x7f || c == 0x08:
								if len(line) > 0 {
									line = line[:len(line)-1]
								}
							case c == 0x03 || c == 0x15 || c == 0x04:
								line = line[:0]
							case c == '\t' || c < 0x20:
							default:
								if len(line) == 0 {
									lineStart = t
								}
								line = append(line, c)
							}
						}
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	return out, nil
}
