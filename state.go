package multiline

// Advance describes a single transition in a [State]: when Pattern (a regular
// expression) matches the current line, the state machine moves to the state
// named Next.
type Advance struct {
	Pattern string
	Next    string
}

// State is one node in a multi-line matcher. Name may list several comma-separated
// names so that transitions shared by multiple states can be declared once. A state
// is terminal unless NonTerminal is set; a match may only be emitted as an
// aggregated line when it ends on a terminal state. The state named "start_state"
// is the entry point and is where every new group begins.
type State struct {
	Name        string
	NonTerminal bool
	Advance     []Advance
}
