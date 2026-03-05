package agent

import "testing"

func TestParseCodexJSONLineEventMsg(t *testing.T) {
	line := `{"type":"event_msg","payload":{"type":"agent_message","message":"hello"}}`
	got := parseCodexJSONLine(line)
	if got != "hello\n" {
		t.Fatalf("unexpected parsed line: %q", got)
	}
}

func TestParseCodexJSONLineFunctionCallOutput(t *testing.T) {
	line := `{"type":"response_item","payload":{"type":"function_call_output","output":"KODAMA_DONE: ok"}}`
	got := parseCodexJSONLine(line)
	if got != "KODAMA_DONE: ok\n" {
		t.Fatalf("unexpected parsed line: %q", got)
	}
}

func TestParseCodexJSONLineFunctionCall(t *testing.T) {
	line := `{"type":"response_item","payload":{"type":"function_call","name":"exec_command"}}`
	got := parseCodexJSONLine(line)
	if got != "[exec_command]\n" {
		t.Fatalf("unexpected parsed line: %q", got)
	}
}

func TestParseCodexJSONLineItemStarted(t *testing.T) {
	line := `{"type":"item.started","item":{"type":"command_execution","command":"echo hi"}}`
	got := parseCodexJSONLine(line)
	if got != "[exec] echo hi\n" {
		t.Fatalf("unexpected parsed line: %q", got)
	}
}

func TestParseCodexJSONLineItemCompleted(t *testing.T) {
	line := `{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"hello"}}`
	got := parseCodexJSONLine(line)
	if got != "hello\n" {
		t.Fatalf("unexpected parsed line: %q", got)
	}
}

func TestParseCodexJSONLineFallbackExtractsKodamaSignal(t *testing.T) {
	line := `{"type":"unknown","payload":{"text":"some output\\nKODAMA_DONE: shipped\\nmore"}}`
	got := parseCodexJSONLine(line)
	if got != "KODAMA_DONE: shipped\n" {
		t.Fatalf("unexpected parsed line: %q", got)
	}
}
