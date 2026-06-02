package multiline

type advance struct {
	pattern string
	next    string
}

type state struct {
	name        string
	nonTerminal bool
	advance     []advance
}
