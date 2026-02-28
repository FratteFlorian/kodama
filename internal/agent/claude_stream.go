package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// streamEvent is the top-level envelope for --output-format=stream-json lines.
type streamEvent struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Message *streamMessage  `json:"message"`
	// For stream_event (--include-partial-messages)
	Event *streamInnerEvent `json:"event"`
	// For result
	Result  string  `json:"result"`
	IsError bool    `json:"is_error"`
	CostUSD float64 `json:"cost_usd"`
}

type streamMessage struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`            // tool_use: tool name
	Input   json.RawMessage `json:"input"`           // tool_use: input args
	Content []contentBlock  `json:"content"`         // tool_result: nested content
	IsError bool            `json:"is_error"`        // tool_result
}

type streamInnerEvent struct {
	Type  string     `json:"type"`
	Delta *textDelta `json:"delta"`
}

type textDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// parseStreamLine converts one stream-json line into human-readable text to
// emit into the output channel. Returns "" to suppress the line entirely.
func parseStreamLine(line string) string {
	if line == "" || line[0] != '{' {
		return line + "\n"
	}

	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		// Not valid JSON — pass through as-is.
		return line + "\n"
	}

	switch ev.Type {
	case "system":
		// Init event — suppress, just noise.
		return ""

	case "assistant":
		if ev.Message == nil {
			return ""
		}
		var sb strings.Builder
		for _, block := range ev.Message.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					sb.WriteString(block.Text)
					// Ensure text ends with newline so downstream line scanning works.
					if !strings.HasSuffix(block.Text, "\n") {
						sb.WriteByte('\n')
					}
				}
			case "tool_use":
				// Summarise the tool call without dumping the full input JSON.
				input := summariseInput(block.Input)
				sb.WriteString(fmt.Sprintf("[%s%s]\n", block.Name, input))
			}
		}
		return sb.String()

	case "user":
		// Tool results — show a brief summary, not the full content.
		if ev.Message == nil {
			return ""
		}
		var sb strings.Builder
		for _, block := range ev.Message.Content {
			if block.Type == "tool_result" {
				if block.IsError {
					sb.WriteString("[tool error]\n")
				}
				// Don't dump full file contents — too noisy.
			}
		}
		return sb.String()

	case "result":
		if ev.IsError {
			return fmt.Sprintf("[error: %s]\n", ev.Result)
		}
		if ev.CostUSD > 0 {
			return fmt.Sprintf("[completed — cost $%.4f]\n", ev.CostUSD)
		}
		return ""

	case "stream_event":
		// Only emitted with --include-partial-messages. Extract text deltas.
		if ev.Event != nil && ev.Event.Delta != nil && ev.Event.Delta.Type == "text_delta" {
			return ev.Event.Delta.Text
		}
		return ""
	}

	// Unknown event type — suppress.
	return ""
}

// summariseInput extracts a short human-readable label from a tool input JSON.
// E.g. {"file_path": "/foo/bar.go"} → " /foo/bar.go"
func summariseInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	// Try common key names in priority order.
	for _, key := range []string{"file_path", "path", "command", "description", "query"} {
		if v, ok := m[key]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err == nil && s != "" {
				if len(s) > 60 {
					s = s[:57] + "..."
				}
				return " " + s
			}
		}
	}
	return ""
}
