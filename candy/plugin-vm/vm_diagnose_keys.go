package vm

import (
	"fmt"
	"strings"
)

// TextToGuestKeys turns a line of text into the key names a guest console needs, so a
// diagnosis is `charly vm type <vm> "systemctl status sshd"` instead of forty send-key
// calls.
//
// It covers the printable ASCII an operator actually types at a rescue console. Anything
// outside that is an ERROR naming the character, not a silent drop: a command that arrives
// on the guest with a character missing runs something other than what was asked, and the
// operator has no way to see that from here.
func TextToGuestKeys(text string) ([]string, error) {
	var keys []string
	for _, r := range text {
		k, err := runeToGuestKey(r)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// shiftedASCII maps a shifted printable to its unshifted key name.
var shiftedASCII = map[rune]string{
	'!': "1", '@': "2", '#': "3", '$': "4", '%': "5", '^': "6", '&': "7", '*': "8",
	'(': "9", ')': "0", '_': "minus", '+': "equal", '{': "bracket_left",
	'}': "bracket_right", '|': "backslash", ':': "semicolon", '"': "apostrophe",
	'<': "comma", '>': "dot", '?': "slash", '~': "grave_accent",
}

// plainASCII maps an unshifted printable to its key name.
var plainASCII = map[rune]string{
	' ': "spc", '-': "minus", '=': "equal", '[': "bracket_left", ']': "bracket_right",
	'\\': "backslash", ';': "semicolon", '\'': "apostrophe", ',': "comma",
	'.': "dot", '/': "slash", '`': "grave_accent", '\n': "ret", '\t': "tab",
}

func runeToGuestKey(r rune) (string, error) {
	switch {
	case r >= 'a' && r <= 'z':
		return string(r), nil
	case r >= 'A' && r <= 'Z':
		return "shift-" + strings.ToLower(string(r)), nil
	case r >= '0' && r <= '9':
		return string(r), nil
	}
	if k, ok := plainASCII[r]; ok {
		return k, nil
	}
	if k, ok := shiftedASCII[r]; ok {
		return "shift-" + k, nil
	}
	return "", fmt.Errorf("cannot type %q on a guest console: only printable ASCII is supported, and dropping it "+
		"silently would run a different command than the one asked for", r)
}

// virshKeyNames maps qemu-monitor key names — the ones operators, docs and this codebase's
// own notes use — to the X11-style names `virsh send-key` takes.
//
// The two vocabularies overlap but disagree on exactly the keys a rescue session needs
// (ret, spc, the punctuation), so the translation lives HERE, once, rather than being
// re-derived at each call site.
var virshKeyNames = map[string]string{
	"ret": "KEY_ENTER", "spc": "KEY_SPACE", "tab": "KEY_TAB", "esc": "KEY_ESC",
	"minus": "KEY_MINUS", "equal": "KEY_EQUAL", "bracket_left": "KEY_LEFTBRACE",
	"bracket_right": "KEY_RIGHTBRACE", "backslash": "KEY_BACKSLASH",
	"semicolon": "KEY_SEMICOLON", "apostrophe": "KEY_APOSTROPHE", "comma": "KEY_COMMA",
	"dot": "KEY_DOT", "slash": "KEY_SLASH", "grave_accent": "KEY_GRAVE",
	"backspace": "KEY_BACKSPACE", "delete": "KEY_DELETE",
	"up": "KEY_UP", "down": "KEY_DOWN", "left": "KEY_LEFT", "right": "KEY_RIGHT",
	"shift": "KEY_LEFTSHIFT", "ctrl": "KEY_LEFTCTRL", "alt": "KEY_LEFTALT",
}

// guestKeyToVirsh translates one monitor-style key (optionally with modifiers, e.g.
// "alt-f2", "ctrl-alt-delete", "shift-a") into the virsh send-key argument list.
func guestKeyToVirsh(key string) ([]string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("empty key")
	}
	parts := strings.Split(key, "-")
	// "grave_accent" and friends contain no dash; a single part is the common case.
	if len(parts) == 1 {
		code, err := oneGuestKeyToVirsh(parts[0])
		if err != nil {
			return nil, err
		}
		return []string{code}, nil
	}
	// A dashed name may be modifiers + key, OR a single key whose NAME contains a dash is
	// not a thing in this vocabulary — every multi-part name is modifiers plus one key.
	var out []string
	for _, p := range parts {
		code, err := oneGuestKeyToVirsh(p)
		if err != nil {
			return nil, fmt.Errorf("in key %q: %w", key, err)
		}
		out = append(out, code)
	}
	return out, nil
}

func oneGuestKeyToVirsh(p string) (string, error) {
	if code, ok := virshKeyNames[p]; ok {
		return code, nil
	}
	switch {
	case len(p) == 1 && p[0] >= 'a' && p[0] <= 'z':
		return "KEY_" + strings.ToUpper(p), nil
	case len(p) == 1 && p[0] >= '0' && p[0] <= '9':
		return "KEY_" + p, nil
	case len(p) >= 2 && p[0] == 'f':
		// function keys f1..f12
		rest := p[1:]
		ok := true
		for _, c := range rest {
			if c < '0' || c > '9' {
				ok = false
				break
			}
		}
		if ok {
			return "KEY_F" + rest, nil
		}
	}
	return "", fmt.Errorf("unknown key %q", p)
}
