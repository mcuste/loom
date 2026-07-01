package executor

// traceSink is an executor-internal observation seam for interpreter-level
// events. It stays private so the architecture can settle before any public
// tracing API is committed.
type traceSink interface {
	ProgramStart(*program)
	UnitStart(unit)
	NodeStart(*node)
	NodeFinish(*node, TaskResult, error)
	LoopPassStart(*loopProgram, int)
	LoopPassFinish(*loopProgram, int, error)
}
