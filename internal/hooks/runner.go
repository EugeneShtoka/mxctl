package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// PipeConfig describes a single step in a plugin's pipe chain.
type PipeConfig struct {
	Cmd    string         `toml:"cmd"`
	Config map[string]any `toml:"config,omitempty"`
}

// PluginConfig describes a plugin and its pipe chain.
//
// Termination controls whether a completed plugin stops further plugin processing:
//
//	termination_code N          — stop if any pipe exits with code N
//	inverted_termination_code N — stop if any pipe exits with any code other than N
//
// Omitting a field (or leaving it null) disables that termination condition.
// Both fields may be set; either matching condition triggers termination.
//
// exclude_self, when true, appends the globally-configured self_ids to the
// blacklist field of every pipe's config before the pipe is invoked.
// exclude_spammers works the same way using the globally-configured spammer_ids.
type PluginConfig struct {
	Name                    string       `toml:"name"`
	Pipes                   []PipeConfig `toml:"pipes"`
	ExcludeSelf             bool         `toml:"exclude_self,omitempty"`
	ExcludeSpammers         bool         `toml:"exclude_spammers,omitempty"`
	TerminationCode         *int         `toml:"termination_code,omitempty"`
	InvertedTerminationCode *int         `toml:"inverted_termination_code,omitempty"`
}

// Event is the payload delivered to each plugin.
type Event struct {
	EventID    string `json:"event_id"`
	RoomID     string `json:"room_id"`
	RoomName   string `json:"room_name"`
	Sender     string `json:"sender"`
	SenderName string `json:"sender_name"`
	Body       string `json:"body"`
	MsgType    string `json:"msg_type"`
	TS         int64  `json:"ts"`
	Severity   string `json:"severity,omitempty"`
	Color      string `json:"color,omitempty"`
}

// Send runs each plugin's pipe chain for the given event.
// selfIDs and spammerIDs are global ID lists injected into the blacklist of
// each pipe's config for plugins that have exclude_self / exclude_spammers set.
// If a plugin's termination condition is met, no further plugins are run.
func Send(plugins []PluginConfig, evt *Event, selfIDs, spammerIDs []string) {
	eventJSON, err := json.Marshal(evt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal event: %v\n", err)
		return
	}
	eventArg := string(eventJSON)

	for _, plugin := range plugins {
		_, terminate := runPipeChain(plugin, eventArg, eventJSON, selfIDs, spammerIDs)
		if terminate {
			break
		}
	}
}

// runPipeChain executes the pipe chain for a single plugin.
// Returns ok=true if all pipes exit 0, and terminate=true if the plugin's
// termination condition was met.
func runPipeChain(plugin PluginConfig, eventArg string, eventJSON []byte, selfIDs, spammerIDs []string) (ok bool, terminate bool) {
	accumulated := make(map[string]any)
	if err := json.Unmarshal(eventJSON, &accumulated); err != nil {
		fmt.Fprintf(os.Stderr, "plugin %q: unmarshal event: %v\n", plugin.Name, err)
		return false, shouldTerminate(plugin, -1)
	}

	for i, pipe := range plugin.Pipes {
		// shallow copy so injected blacklist entries don't persist across events
		config := make(map[string]any, len(pipe.Config))
		for k, v := range pipe.Config {
			config[k] = v
		}
		if plugin.ExcludeSelf && len(selfIDs) > 0 {
			withBlacklist(config, selfIDs)
		}
		if plugin.ExcludeSpammers && len(spammerIDs) > 0 {
			withBlacklist(config, spammerIDs)
		}

		cfgJSON, err := json.Marshal(config)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plugin %q pipe %d: marshal config: %v\n", plugin.Name, i, err)
			return false, shouldTerminate(plugin, -1)
		}
		cmdStr := expandHome(pipe.Cmd) + " --event " + shellQuote(eventArg) + " --config " + shellQuote(string(cfgJSON))

		accJSON, _ := json.Marshal(accumulated)
		cmd := exec.Command("sh", "-c", cmdStr) //nolint:gosec
		cmd.Stdin = bytes.NewReader(accJSON)
		cmd.Stderr = os.Stderr

		out, err := cmd.Output()
		if err != nil {
			code := -1
			if ee, ok2 := err.(*exec.ExitError); ok2 {
				code = ee.ExitCode()
			}
			if code != 2 {
				// Exit code 2 means the pipe intentionally filtered the event — not an error.
				fmt.Fprintf(os.Stderr, "plugin %q pipe %d: %v\n", plugin.Name, i, err)
			}
			return false, shouldTerminate(plugin, code)
		}

		if trimmed := bytes.TrimSpace(out); len(trimmed) > 0 {
			var delta map[string]any
			if err := json.Unmarshal(trimmed, &delta); err != nil {
				fmt.Fprintf(os.Stderr, "plugin %q pipe %d: invalid JSON output: %v\n", plugin.Name, i, err)
				return false, shouldTerminate(plugin, -1)
			}
			for k, v := range delta {
				accumulated[k] = v
			}
		}
	}
	return true, shouldTerminate(plugin, 0)
}

// withBlacklist appends ids to the blacklist field of config in place.
func withBlacklist(config map[string]any, ids []string) {
	existing, _ := config["blacklist"].([]any)
	for _, id := range ids {
		existing = append(existing, id)
	}
	config["blacklist"] = existing
}

// shouldTerminate reports whether the given pipe exit code satisfies the
// plugin's termination condition.
func shouldTerminate(plugin PluginConfig, code int) bool {
	if plugin.TerminationCode != nil && code == *plugin.TerminationCode {
		return true
	}
	if plugin.InvertedTerminationCode != nil && code != *plugin.InvertedTerminationCode {
		return true
	}
	return false
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return home + path[1:]
	}
	return path
}
