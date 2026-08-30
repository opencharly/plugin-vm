package vm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// A minimal QMP client for the two human-monitor commands the diagnosis verbs need
// (`screendump`, `sendkey`).
//
// govmm's QMP client — already vendored and used by vm_qemu.go for system_powerdown —
// exposes a fixed catalogue of typed commands and **no** generic execute, so neither of
// these is reachable through it. The alternative that was actually being used by hand is
// `socat` piping monitor text into the socket, which is not a charly dependency and gives
// no way to tell a refused command from a silent one.
//
// The protocol is a JSON line stream: greeting, qmp_capabilities, then commands. Async
// `event` messages interleave at any point and are skipped — reading one as a reply is the
// classic bug in a hand-rolled client, so it is handled here once instead of at each call.

type qmpConn struct {
	c  net.Conn
	br *bufio.Reader
}

func dialQMP(socketPath string, timeout time.Duration) (*qmpConn, error) {
	c, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return nil, fmt.Errorf("connecting to the QMP socket %s: %w", socketPath, err)
	}
	if err := c.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = c.Close()
		return nil, err
	}
	q := &qmpConn{c: c, br: bufio.NewReader(c)}
	// The greeting is the first line; anything else means this is not a QMP socket.
	var greeting struct {
		QMP *json.RawMessage `json:"QMP"`
	}
	if err := q.readInto(&greeting); err != nil {
		_ = c.Close()
		return nil, err
	}
	if greeting.QMP == nil {
		_ = c.Close()
		return nil, fmt.Errorf("%s did not send a QMP greeting — it is not a QMP monitor socket", socketPath)
	}
	if _, err := q.execute("qmp_capabilities", nil); err != nil {
		_ = c.Close()
		return nil, err
	}
	return q, nil
}

func (q *qmpConn) Close() error { return q.c.Close() }

func (q *qmpConn) readInto(v any) error {
	line, err := q.br.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("reading from the QMP socket: %w", err)
	}
	return json.Unmarshal(line, v)
}

// execute sends one command and returns its `return` value, skipping async events.
func (q *qmpConn) execute(cmd string, args map[string]any) (json.RawMessage, error) {
	req := map[string]any{"execute": cmd}
	if args != nil {
		req["arguments"] = args
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := q.c.Write(append(body, '\n')); err != nil {
		return nil, fmt.Errorf("sending %s over QMP: %w", cmd, err)
	}
	for {
		var reply struct {
			Return json.RawMessage `json:"return"`
			Error  *struct {
				Class string `json:"class"`
				Desc  string `json:"desc"`
			} `json:"error"`
			Event string `json:"event"`
		}
		if err := q.readInto(&reply); err != nil {
			return nil, err
		}
		if reply.Event != "" {
			continue // async guest event — not our reply
		}
		if reply.Error != nil {
			return nil, fmt.Errorf("QMP %s: %s: %s", cmd, reply.Error.Class, reply.Error.Desc)
		}
		return reply.Return, nil
	}
}

// humanMonitor runs one human-monitor command and returns its text output.
//
// qemu reports monitor-level failures INSIDE that text with a zero QMP status, so the text
// is returned to the caller to inspect rather than discarded — a `sendkey` naming an unknown
// key otherwise looks exactly like a successful one.
func (q *qmpConn) humanMonitor(line string) (string, error) {
	raw, err := q.execute("human-monitor-command", map[string]any{"command-line": line})
	if err != nil {
		return "", err
	}
	var out string
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", fmt.Errorf("unexpected human-monitor-command reply %s: %w", string(raw), err)
		}
	}
	return out, nil
}
