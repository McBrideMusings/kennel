package main

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// tomlArray is one array of strings found in config.toml's text, so share add
// and rm can change it without re-encoding the file: BurntSushi/toml drops
// comments and key order on encode. open and close are the offsets of [ and ].
type tomlArray struct {
	src         []byte
	open, close int
	elems       []tomlElem
}

// tomlElem is one string in the array: its value and the offsets of its
// quoted text.
type tomlElem struct {
	value      string
	start, end int
}

// findShareArray locates `key = [...]` inside the [shares.<name>] table.
// It reads only that layout; a share written as an inline table or with
// dotted keys is an error that asks for a hand edit.
func findShareArray(src []byte, name, key string) (*tomlArray, error) {
	start, end, err := findShareLine(src, name, key)
	if err != nil {
		return nil, err
	}
	i := start + bytes.IndexByte(src[start:end], '=') + 1
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	if i >= len(src) || src[i] != '[' {
		return nil, fmt.Errorf("[shares.%s] %s is not an array of strings; edit it by hand", name, key)
	}
	return scanArray(src, i, name, key)
}

// findShareLine returns the offsets of the line holding `key = ` inside the
// [shares.<name>] table, its newline included.
func findShareLine(src []byte, name, key string) (start, end int, err error) {
	lines := bytes.SplitAfter(src, []byte("\n"))
	off, inTable := 0, false
	for _, line := range lines {
		start := off
		off += len(line)
		t := strings.TrimSpace(stripComment(string(line)))
		if strings.HasPrefix(t, "[") && !inTable {
			h := strings.Join(strings.Fields(t), "")
			inTable = h == "[shares."+name+"]" || h == `[shares."`+name+`"]`
			continue
		}
		if !inTable {
			continue
		}
		if strings.HasPrefix(t, "[") {
			break
		}
		rest, ok := strings.CutPrefix(t, key)
		if ok && strings.HasPrefix(strings.TrimLeft(rest, " \t"), "=") {
			return start, off, nil
		}
	}
	return 0, 0, fmt.Errorf("no `%s = [...]` line in [shares.%s]; edit it by hand", key, name)
}

// insertShareArray returns src with a new line `key = ["v"]` right after the
// [shares.<name>] table's `after = ` line, indented like it.
func insertShareArray(src []byte, name, after, key, v string) ([]byte, error) {
	start, end, err := findShareLine(src, name, after)
	if err != nil {
		return nil, err
	}
	line := src[start:end]
	indent := line[:len(line)-len(bytes.TrimLeft(line, " \t"))]
	add := string(indent) + key + " = [" + strconv.Quote(v) + "]\n"
	if !bytes.HasSuffix(line, []byte("\n")) {
		add = "\n" + strings.TrimSuffix(add, "\n")
	}
	out := append([]byte{}, src[:end]...)
	out = append(out, add...)
	return append(out, src[end:]...), nil
}

// scanArray reads the strings, commas and comments of the array that opens
// at src[open].
func scanArray(src []byte, open int, name, key string) (*tomlArray, error) {
	a := &tomlArray{src: src, open: open}
	bad := fmt.Errorf("[shares.%s] %s is not a plain array of strings; edit it by hand", name, key)
	for i := open + 1; i < len(src); i++ {
		switch c := src[i]; c {
		case ' ', '\t', '\r', '\n', ',':
		case '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case ']':
			a.close = i
			return a, nil
		case '"', '\'':
			j := i + 1
			for j < len(src) && src[j] != c && src[j] != '\n' {
				if c == '"' && src[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(src) || src[j] != c {
				return nil, bad
			}
			raw := string(src[i : j+1])
			v := raw[1 : len(raw)-1]
			if c == '"' {
				var err error
				if v, err = strconv.Unquote(raw); err != nil {
					return nil, bad
				}
			}
			a.elems = append(a.elems, tomlElem{value: v, start: i, end: j + 1})
			i = j
		default:
			return nil, bad
		}
	}
	return nil, bad
}

// stripComment drops a # comment outside quotes from one line.
func stripComment(line string) string {
	var q byte
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case q == 0 && c == '#':
			return line[:i]
		case q == 0 && (c == '"' || c == '\''):
			q = c
		case q == '"' && c == '\\':
			i++
		case c == q:
			q = 0
		}
	}
	return line
}

func (a *tomlArray) multiline() bool {
	return bytes.IndexByte(a.src[a.open:a.close], '\n') >= 0
}

// splice returns src with src[from:to] replaced by s.
func (a *tomlArray) splice(from, to int, s string) []byte {
	out := append([]byte{}, a.src[:from]...)
	out = append(out, s...)
	return append(out, a.src[to:]...)
}

// appended returns the file text with v added as the array's last string,
// in the layout the array already has: same line, or a new line indented
// like the last one.
func (a *tomlArray) appended(v string) []byte {
	q := strconv.Quote(v)
	if len(a.elems) == 0 {
		return a.splice(a.close, a.close, q)
	}
	last := a.elems[len(a.elems)-1]
	if !a.multiline() {
		return a.splice(last.end, last.end, ", "+q)
	}
	lineStart := bytes.LastIndexByte(a.src[:last.start], '\n') + 1
	line := a.src[lineStart:last.start]
	indent := line[:len(line)-len(bytes.TrimLeft(line, " \t"))]
	eol := last.end + bytes.IndexByte(a.src[last.end:], '\n')
	if eol < last.end || eol > a.close {
		// the last string shares its line with the closing ]
		return a.splice(last.end, last.end, ",\n"+string(indent)+q)
	}
	text := a.src[last.end:eol]
	comma := ""
	if !bytes.HasPrefix(bytes.TrimLeft(text, " \t"), []byte(",")) {
		comma = ","
	}
	out := a.splice(last.end, last.end, comma)
	at := eol + len(comma)
	return append(out[:at:at], append([]byte("\n"+string(indent)+q+","), out[at:]...)...)
}

// removed returns the file text without the array's i-th string. A string on
// a line of its own goes with its whole line, comment included; one sharing a
// line goes with one neighbouring comma.
func (a *tomlArray) removed(i int) []byte {
	e := a.elems[i]
	after := e.end
	for after < a.close && (a.src[after] == ' ' || a.src[after] == '\t') {
		after++
	}
	hasComma := after < a.close && a.src[after] == ','
	if hasComma {
		after++
	}
	lineStart := bytes.LastIndexByte(a.src[:e.start], '\n') + 1
	eol := after + bytes.IndexByte(a.src[after:], '\n')
	if eol >= after && eol < a.close &&
		len(bytes.TrimSpace(a.src[lineStart:e.start])) == 0 &&
		len(bytes.TrimSpace(stripCommentBytes(a.src[after:eol]))) == 0 {
		return a.splice(lineStart, eol+1, "")
	}
	if hasComma {
		for after < a.close && a.src[after] == ' ' {
			after++
		}
		return a.splice(e.start, after, "")
	}
	from := e.start
	if i > 0 {
		from = a.elems[i-1].end
	}
	return a.splice(from, e.end, "")
}

func stripCommentBytes(b []byte) []byte { return []byte(stripComment(string(b))) }
