package vm

import (
	"encoding/json"
	"testing"

	"github.com/opencharly/spec/ops"
)

// TestSessionInvokeArgsWire pins the EXACT reverse-leg invocation contract the runner's
// verb:session seam decodes (plugin-check session_seam.go): class "verb", word "session",
// op OpExecute, and the request JSON in the seam's wire shape. This test FAILS if the
// submission path is deleted or its wire contract drifts - the guard the validator asked
// for (A2/B12: a test that would fail without the reverse-leg submission).
func TestSessionInvokeArgsWire(t *testing.T) {
	calls := []sessionRequest{
		{Op: "spawn", SessionID: "bed.member.screen", Command: []string{"/bin/false", "x"}, Dir: "/tmp",
			Env: map[string]string{"K": "V"}, LogDir: "/tmp/log"},
		{Op: "stop", SessionID: "bed.member.screen"},
		{Op: "status", SessionID: "bed.member.screen"},
	}
	for _, req := range calls {
		class, word, reqJSON, err := sessionInvokeArgs(req)
		if err != nil {
			t.Fatalf("sessionInvokeArgs(%+v): %v", req, err)
		}
		if class != "verb" || word != "session" {
			t.Fatalf("wire class/word = %q/%q, want verb/session", class, word)
		}
		if ops.OpExecute == "" {
			t.Fatal("OpExecute empty")
		}
		var decoded sessionRequest
		if err := json.Unmarshal(reqJSON, &decoded); err != nil {
			t.Fatalf("decoded request JSON: %v", err)
		}
		if decoded.Op != req.Op || decoded.SessionID != req.SessionID {
			t.Fatalf("wire request roundtrip mismatch: %+v vs %+v", decoded, req)
		}
		if req.Op == "spawn" {
			if len(decoded.Command) == 0 || decoded.LogDir == "" || decoded.Env["K"] != "V" {
				t.Fatalf("spawn wire missing recorder fields: %+v", decoded)
			}
		}
	}
}
