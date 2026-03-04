package agent

// Signal represents a structured communication signal from an agent.
type Signal int

const (
	SignalNone        Signal = iota
	SignalQuestion           // KODAMA_QUESTION:
	SignalDone               // KODAMA_DONE:
	SignalRateLimited        // KODAMA_RATELIMIT:
	SignalBlocked            // KODAMA_BLOCKED:
	SignalPR                 // KODAMA_PR:
	SignalDecision           // KODAMA_DECISION:
)

// Agent is the interface that all coding agents must implement.
type Agent interface {
	// Start launches the agent with the given task in the given working directory.
	// contextFile is the path to kodama.md (may be empty).
	Start(workdir, task, contextFile string) error

	// Write sends input to the agent's stdin (e.g. answering a question).
	Write(input string) error

	// Output returns a channel that streams output lines from the agent.
	Output() <-chan string

	// Detect parses a line of output and returns any structured signal and its payload.
	Detect(line string) (Signal, string)

	// Stop terminates the agent process.
	Stop() error

	// SessionID returns the agent's session ID if the backend supports sessions
	// (used to resume a conversation after answering a KODAMA_QUESTION).
	// Returns "" if sessions are not supported.
	SessionID() string

	// CostUSD returns the total API cost in USD for the last run, if available.
	CostUSD() float64

	// TokensUsed returns the input and output token counts for the last run, if available.
	TokensUsed() (inputTokens, outputTokens int64)

	// LastError returns the last process error (if any) after the agent exits.
	LastError() error
}
