// event.go — typed event produced by each iteration of the DecisionLoop.
package nerve

// Event is a typed event produced by each iteration of the DecisionLoop.
// The host consumes events via a yield function.
//
// Each EventKind uses specific fields; unused fields are zero values.
// This is intentional — the flat struct is passed by value on the stack
// via yield, avoiding heap allocation that an interface-based design would incur.
type Event struct {
	Kind     EventKind
	Text     string    // KindText: LLM text output
	ToolCall *ToolCall // KindToolCall: LLM decided to call a tool
	Effect   *Effect   // KindToolResult: tool execution result
	State    LoopState // KindState: loop state change
	Err      error     // KindError: unrecoverable error
	Output   string    // KindDone: final accumulated output
	Usage    *Usage    // KindUsage: token usage of the last Think
}

// EventKind categorizes the type of event.
type EventKind int

const (
	EventText       EventKind = iota // LLM text output
	EventToolCall                    // LLM decided to call a tool
	EventToolResult                  // Tool execution result
	EventState                       // Loop state change
	EventDone                        // Loop completed normally
	EventError                       // Loop encountered unrecoverable error
	EventUsage                       // Token usage of the last Think
)
