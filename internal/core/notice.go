package core

// Notice is a system message (announcement, report, confirmation). Surfaces
// render it like an answer: a plan header holding one task, then the body.
type Notice struct {
	Title  string // plan title, e.g. "Self-update"
	Task   string // one-line status, e.g. "Update applied and stable"
	Status string // complete (default) | error | in_progress
	Detail string // short plain-text output of the task
	Body   string // optional markdown text shown below the plan
}
