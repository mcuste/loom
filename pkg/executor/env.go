package executor

import (
	"os"
	"strconv"

	"github.com/mcuste/loom/pkg/workflow"
)

// validEnvName reports whether s is a usable POSIX shell variable name. Task
// ids, param names, state keys, and the for_each loop variable share the
// identifier class [A-Za-z0-9_]+, which permits a leading digit that a shell
// variable name forbids; such a name is skipped, since it could never be
// referenced as $name from the command anyway.
func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		alpha := r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
		digit := r >= '0' && r <= '9'
		if i == 0 && !alpha {
			return false
		}
		if !alpha && !digit {
			return false
		}
	}
	return true
}

// taskEnv builds the child environment for a shell or script task: the parent
// environment plus a flat, bare-named entry for every upstream value, so a
// command can read an output as "$id" instead of splicing {{id}} into the shell
// line. Passing values through the environment (rather than into the command
// string) makes output containing shell metacharacters (backticks, $(...),
// quotes, newlines) inert, which textual {{...}} substitution cannot guarantee.
//
// The bare scheme is ergonomic but flat: an output `id`, the same-named param,
// and the for_each loop variable share one namespace, and a task named like a
// real variable (PATH, HOME) shadows it for the child. Entries are appended in a
// fixed precedence and exec takes the last value for a duplicate key, so the
// result is deterministic; later wins: params, state, prev-iteration outputs,
// exit codes (as "<id>_exit"), current outputs, then the loop variable.
func taskEnv(
	outputs map[workflow.TaskID]string,
	params workflow.ParamValues,
	state map[string]string,
	prev map[workflow.TaskID]string,
	exitCodes map[workflow.TaskID]int,
	loopVar, loopVal string,
) []string {
	env := os.Environ()
	add := func(name, val string) {
		if validEnvName(name) {
			env = append(env, name+"="+val)
		}
	}
	for name, v := range params {
		add(string(name), v)
	}
	for k, v := range state {
		add(k, v)
	}
	for id, v := range prev {
		add("prev_"+string(id), v)
	}
	for id, code := range exitCodes {
		add(string(id)+"_exit", strconv.Itoa(code))
	}
	for id, v := range outputs {
		add(string(id), v)
	}
	if loopVar != "" {
		add(loopVar, loopVal)
	}
	return env
}
