package aqaramcp

import (
	"fmt"
	"strings"
)

// Status is the current state of one device: the flat key/value text the
// platform reports, such as {"lock_state": "1", "online_offline": "online"}.
// Which keys appear depends on the device type; the helpers read the common
// ones.
type Status map[string]string

// Keys the platform uses in a Status. Every device carries KeyOnline; the
// rest depend on what the device is.
const (
	// KeyOnline is "online" or "offline".
	KeyOnline = "online_offline"
	// KeyWaterLeak is "True" while a leak sensor is wet.
	KeyWaterLeak = "water_leak"
	// KeyOnOff is "on" or "off" for outlets and switches.
	KeyOnOff = "on_off"
	// KeyLockState is a lock's state code. "1" is observed while the lock is
	// locked; the platform does not document the other values.
	KeyLockState = "lock_state"
)

// statusParser walks the platform's rendering of a status, which is a Python
// dictionary literal rather than JSON: single-quoted keys and values, and
// True/False spelt the Python way when they appear bare.
type statusParser struct {
	src string
	pos int
}

// Get returns one value and whether the device reports that key at all.
func (s Status) Get(key string) (string, bool) {
	v, ok := s[key]
	return v, ok
}

// Online reports whether the platform could reach the device.
func (s Status) Online() bool {
	return s[KeyOnline] == "online"
}

// Bool reads a flag, accepting the spellings the platform uses across device
// types: True/False, on/off, 1/0.
func (s Status) Bool(key string) (bool, error) {
	v, ok := s[key]
	if !ok {
		return false, fmt.Errorf("status has no %q", key)
	}
	switch strings.ToLower(v) {
	case "true", "on", "1":
		return true, nil
	case "false", "off", "0":
		return false, nil
	default:
		return false, fmt.Errorf("status %s=%q: not a flag", key, v)
	}
}

// parseStatus decodes a status column. The grammar is the subset of Python
// literals the platform emits: a dictionary of strings, with the odd bare
// token such as True or None kept as its text, and any nested dictionary or
// list kept whole as its text rather than decoded.
func parseStatus(text string) (Status, error) {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, fmt.Errorf("status %q: not a dictionary", text)
	}
	p := &statusParser{src: trimmed[1 : len(trimmed)-1]}
	status := Status{}
	for {
		p.skipSpace()
		if p.done() {
			return status, nil
		}
		key, err := p.quoted()
		if err != nil {
			return nil, fmt.Errorf("status %q: key: %w", text, err)
		}
		p.skipSpace()
		if !p.accept(':') {
			return nil, fmt.Errorf("status %q: expected ':' after %q", text, key)
		}
		p.skipSpace()
		value, err := p.value()
		if err != nil {
			return nil, fmt.Errorf("status %q: value of %q: %w", text, key, err)
		}
		status[key] = value
		p.skipSpace()
		if p.done() {
			return status, nil
		}
		if !p.accept(',') {
			return nil, fmt.Errorf("status %q: expected ',' after %q", text, key)
		}
	}
}

func (p *statusParser) done() bool {
	return p.pos >= len(p.src)
}

func (p *statusParser) skipSpace() {
	for !p.done() && strings.IndexByte(" \t\r\n", p.src[p.pos]) >= 0 {
		p.pos++
	}
}

// accept consumes the byte if it is next.
func (p *statusParser) accept(b byte) bool {
	if p.done() || p.src[p.pos] != b {
		return false
	}
	p.pos++
	return true
}

// value reads a quoted string, a nested literal kept as text, or a bare token
// running up to the next comma.
func (p *statusParser) value() (string, error) {
	if p.done() {
		return "", fmt.Errorf("missing")
	}
	switch p.src[p.pos] {
	case '\'', '"':
		return p.quoted()
	case '{', '[':
		return p.nested()
	}
	start := p.pos
	for !p.done() && p.src[p.pos] != ',' {
		p.pos++
	}
	token := strings.TrimSpace(p.src[start:p.pos])
	if token == "" {
		return "", fmt.Errorf("missing")
	}
	return token, nil
}

// nested reads a bracketed literal — a dictionary or a list — whole, as its
// text. Brackets and commas inside quoted strings do not count, so the
// strings are skipped rather than scanned.
func (p *statusParser) nested() (string, error) {
	start := p.pos
	depth := 0
	for !p.done() {
		switch p.src[p.pos] {
		case '{', '[':
			depth++
			p.pos++
		case '}', ']':
			depth--
			p.pos++
			if depth == 0 {
				return p.src[start:p.pos], nil
			}
		case '\'', '"':
			if _, err := p.quoted(); err != nil {
				return "", err
			}
		default:
			p.pos++
		}
	}
	return "", fmt.Errorf("unterminated %c", p.src[start])
}

// quoted reads a string in either kind of quote, honouring backslash escapes.
func (p *statusParser) quoted() (string, error) {
	if p.done() || (p.src[p.pos] != '\'' && p.src[p.pos] != '"') {
		return "", fmt.Errorf("expected a quoted string at offset %d", p.pos)
	}
	quote := p.src[p.pos]
	p.pos++
	var b strings.Builder
	for !p.done() {
		ch := p.src[p.pos]
		p.pos++
		switch ch {
		case quote:
			return b.String(), nil
		case '\\':
			if p.done() {
				return "", fmt.Errorf("unterminated escape")
			}
			esc := p.src[p.pos]
			p.pos++
			switch esc {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(esc)
			}
		default:
			b.WriteByte(ch)
		}
	}
	return "", fmt.Errorf("unterminated string")
}
