package hooks

import (
	"encoding/json"
	"os"
	"testing"
)

func intPtr(n int) *int { return &n }

// wrap nests a shell command inside sh -c '...' so the --event and --config
// flags appended by the runner become positional parameters ($0, $1...) of
// the inner shell rather than flags to the actual command.
func wrap(s string) string { return "sh -c " + shellQuote(s) }

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()

	cases := []struct {
		in, want string
	}{
		{"~/foo/bar", home + "/foo/bar"},
		{"/abs/path", "/abs/path"},
		{"relative/path", "relative/path"},
		{"~noSlash", "~noSlash"},
	}
	for _, c := range cases {
		if got := expandHome(c.in); got != c.want {
			t.Errorf("expandHome(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{`hello`, `'hello'`},
		{`it's`, `'it'\''s'`},
		{`{"key":"value"}`, `'{"key":"value"}'`},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEventJSON(t *testing.T) {
	evt := &Event{
		EventID:  "evt1",
		RoomID:   "!room:server",
		RoomName: "Test Room",
		Sender:   "@user:server",
		Body:     "hello",
		MsgType:  "m.text",
		TS:       1700000000000,
	}
	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"event_id", "room_id", "room_name", "sender", "body", "msg_type", "ts"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing key %q in event JSON", key)
		}
	}
}

func TestSendNoPlugins(t *testing.T) {
	Send(nil, &Event{Body: "hi"}, nil, nil)
}

func TestSendSinglePipe(t *testing.T) {
	Send([]PluginConfig{
		{Name: "sink", Pipes: []PipeConfig{{Cmd: wrap(`cat > /dev/null`)}}},
	}, &Event{EventID: "e1", RoomName: "Test", Sender: "@u:s", Body: "hello"}, nil, nil)
}

func TestSendMultiplePlugins(t *testing.T) {
	Send([]PluginConfig{
		{Name: "a", Pipes: []PipeConfig{{Cmd: wrap(`cat > /dev/null`)}}},
		{Name: "b", Pipes: []PipeConfig{{Cmd: wrap(`cat > /dev/null`)}}},
	}, &Event{Body: "hi"}, nil, nil)
}

func TestSendPluginWithConfig(t *testing.T) {
	Send([]PluginConfig{
		{Name: "cfg", Pipes: []PipeConfig{{Cmd: wrap(`cat > /dev/null`), Config: map[string]any{"urgency": "normal"}}}},
	}, &Event{Body: "hi"}, nil, nil)
}

func TestPipeChainAccumulation(t *testing.T) {
	Send([]PluginConfig{
		{Name: "chain", Pipes: []PipeConfig{
			{Cmd: wrap(`printf '{"extra":"added"}'`)},
			{Cmd: wrap(`jq -e '.extra == "added"' > /dev/null`)},
		}},
	}, &Event{Body: "test"}, nil, nil)
}

func TestPipeChainFailureAbortsChain(t *testing.T) {
	tmpFile := t.TempDir() + "/ran"
	Send([]PluginConfig{
		{Name: "fail-chain", Pipes: []PipeConfig{
			{Cmd: wrap(`exit 1`)},
			{Cmd: wrap(`touch ` + tmpFile)},
		}},
	}, &Event{Body: "test"}, nil, nil)
	if _, err := os.Stat(tmpFile); err == nil {
		t.Error("second pipe ran after first pipe failed")
	}
}

func TestTerminationCode_OnSuccess(t *testing.T) {
	tmpFile := t.TempDir() + "/ran"
	Send([]PluginConfig{
		{Name: "first", Pipes: []PipeConfig{{Cmd: wrap(`cat > /dev/null`)}}, TerminationCode: intPtr(0)},
		{Name: "second", Pipes: []PipeConfig{{Cmd: wrap(`touch ` + tmpFile)}}},
	}, &Event{Body: "test"}, nil, nil)
	if _, err := os.Stat(tmpFile); err == nil {
		t.Error("second plugin ran after terminating plugin succeeded")
	}
}

func TestTerminationCode_OnFiltered(t *testing.T) {
	tmpFile := t.TempDir() + "/ran"
	Send([]PluginConfig{
		{Name: "filter", Pipes: []PipeConfig{{Cmd: wrap(`exit 2`)}}, TerminationCode: intPtr(2)},
		{Name: "second", Pipes: []PipeConfig{{Cmd: wrap(`touch ` + tmpFile)}}},
	}, &Event{Body: "test"}, nil, nil)
	if _, err := os.Stat(tmpFile); err == nil {
		t.Error("second plugin ran after filtered plugin terminated")
	}
}

func TestTerminationCode_NoMatch(t *testing.T) {
	tmpFile := t.TempDir() + "/ran"
	Send([]PluginConfig{
		{Name: "first", Pipes: []PipeConfig{{Cmd: wrap(`cat > /dev/null`)}}, TerminationCode: intPtr(2)},
		{Name: "second", Pipes: []PipeConfig{{Cmd: wrap(`touch ` + tmpFile)}}},
	}, &Event{Body: "test"}, nil, nil)
	if _, err := os.Stat(tmpFile); err != nil {
		t.Error("second plugin did not run — termination triggered incorrectly")
	}
}

func TestInvertedTerminationCode_OnSuccess(t *testing.T) {
	tmpFile := t.TempDir() + "/ran"
	Send([]PluginConfig{
		{Name: "first", Pipes: []PipeConfig{{Cmd: wrap(`cat > /dev/null`)}}, InvertedTerminationCode: intPtr(0)},
		{Name: "second", Pipes: []PipeConfig{{Cmd: wrap(`touch ` + tmpFile)}}},
	}, &Event{Body: "test"}, nil, nil)
	if _, err := os.Stat(tmpFile); err != nil {
		t.Error("second plugin did not run — inverted termination triggered incorrectly on success")
	}
}

func TestInvertedTerminationCode_OnFiltered(t *testing.T) {
	tmpFile := t.TempDir() + "/ran"
	Send([]PluginConfig{
		{Name: "filter", Pipes: []PipeConfig{{Cmd: wrap(`exit 2`)}}, InvertedTerminationCode: intPtr(0)},
		{Name: "second", Pipes: []PipeConfig{{Cmd: wrap(`touch ` + tmpFile)}}},
	}, &Event{Body: "test"}, nil, nil)
	if _, err := os.Stat(tmpFile); err == nil {
		t.Error("second plugin ran — inverted termination should have triggered on filtered event")
	}
}

func TestNoTermination_ByDefault(t *testing.T) {
	tmpFile := t.TempDir() + "/ran"
	Send([]PluginConfig{
		{Name: "first", Pipes: []PipeConfig{{Cmd: wrap(`cat > /dev/null`)}}},
		{Name: "second", Pipes: []PipeConfig{{Cmd: wrap(`touch ` + tmpFile)}}},
	}, &Event{Body: "test"}, nil, nil)
	if _, err := os.Stat(tmpFile); err != nil {
		t.Error("second plugin did not run — unexpected termination")
	}
}

func TestWithBlacklist_AddsToEmpty(t *testing.T) {
	m := map[string]any{}
	withBlacklist(m, []string{"@me:example.com"})
	bl, _ := m["blacklist"].([]any)
	if len(bl) != 1 || bl[0] != "@me:example.com" {
		t.Errorf("unexpected blacklist: %v", bl)
	}
}

func TestWithBlacklist_AppendsToExisting(t *testing.T) {
	m := map[string]any{"blacklist": []any{"@bot:example.com"}}
	withBlacklist(m, []string{"@me:example.com"})
	bl, _ := m["blacklist"].([]any)
	if len(bl) != 2 {
		t.Errorf("expected 2 entries in blacklist, got %d: %v", len(bl), bl)
	}
}

func TestWithBlacklist_PreservesOtherFields(t *testing.T) {
	m := map[string]any{"excluded_rooms": []any{"General"}}
	withBlacklist(m, []string{"@me:example.com"})
	if _, ok := m["excluded_rooms"]; !ok {
		t.Error("existing fields should be preserved after injection")
	}
}

func TestWithBlacklist_EmptyIDs_NoChange(t *testing.T) {
	// Runner skips injection when selfIDs is empty, so withBlacklist is not called.
	// But if it were called with empty ids, result should still be valid.
	m := map[string]any{}
	withBlacklist(m, nil)
	bl, _ := m["blacklist"].([]any)
	if len(bl) != 0 {
		t.Errorf("expected empty blacklist, got: %v", bl)
	}
}
