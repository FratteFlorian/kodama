package agent

import (
	"encoding/json"
	"regexp"
	"strings"
)

type codexEvent struct {
	Type    string          `json:"type"`
	Message string          `json:"message"`
	Payload json.RawMessage `json:"payload"`
	Item    *codexItem      `json:"item"`
}

type codexItem struct {
	Type             string `json:"type"`
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregated_output"`
}

var kodamaSignalInRawRe = regexp.MustCompile(`KODAMA_(QUESTION|DONE|RATELIMIT|BLOCKED|PR|DECISION):`)

// parseCodexJSONLine extracts human-readable text from codex --json events.
func parseCodexJSONLine(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ""
	}
	if trimmed[0] != '{' {
		return line + "\n"
	}

	var ev codexEvent
	if err := json.Unmarshal([]byte(trimmed), &ev); err != nil {
		return line + "\n"
	}

	switch ev.Type {
	case "item.started":
		if ev.Item != nil && ev.Item.Type == "command_execution" && ev.Item.Command != "" {
			return "[exec] " + ev.Item.Command + "\n"
		}
	case "item.completed":
		if ev.Item != nil && ev.Item.Type == "command_execution" && ev.Item.AggregatedOutput != "" {
			out := ev.Item.AggregatedOutput
			if !strings.HasSuffix(out, "\n") {
				out += "\n"
			}
			return out
		}

	case "event_msg":
		var payload struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err == nil && payload.Message != "" {
			return payload.Message + "\n"
		}
		if ev.Message != "" {
			return ev.Message + "\n"
		}
	case "response_item":
		var payload struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Output    string `json:"output"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err == nil {
			switch payload.Type {
			case "message":
				var sb strings.Builder
				for _, c := range payload.Content {
					if c.Type == "output_text" && c.Text != "" {
						sb.WriteString(c.Text)
						if !strings.HasSuffix(c.Text, "\n") {
							sb.WriteByte('\n')
						}
					}
				}
				return sb.String()
			case "function_call":
				if payload.Name != "" {
					return "[" + payload.Name + "]\n"
				}
			case "function_call_output":
				if payload.Output != "" {
					return payload.Output + "\n"
				}
			}
		}
	}

	// Fallback: if Codex event shape changes, still surface protocol signals.
	if text := extractSignalsFromRawJSONLine(trimmed); text != "" {
		return text
	}

	return ""
}

func extractSignalsFromRawJSONLine(raw string) string {
	if !kodamaSignalInRawRe.MatchString(raw) {
		return ""
	}
	// Convert escaped newlines to real newlines to improve splitting.
	decoded := strings.ReplaceAll(raw, `\\n`, "\n")
	lines := strings.Split(decoded, "\n")
	var out strings.Builder
	for _, line := range lines {
		i := strings.Index(line, "KODAMA_")
		if i < 0 {
			continue
		}
		s := line[i:]
		if j := strings.IndexByte(s, '"'); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
		if s != "" {
			out.WriteString(s)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func extractCodexSessionID(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || trimmed[0] != '{' {
		return ""
	}
	var ev codexEvent
	if err := json.Unmarshal([]byte(trimmed), &ev); err != nil {
		return ""
	}
	if ev.Type != "session_meta" {
		return ""
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return ""
	}
	return payload.ID
}
