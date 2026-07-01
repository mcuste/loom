package workflow

import (
	"fmt"
	"regexp"
	"strings"
)

// scanPlaceholders walks text in a SINGLE pass with combinedPlaceholderRe and
// returns the task-id, param, and state placeholder names in source order.
func scanPlaceholders(text string) (taskRefs, paramRefs, stateRefs []string) {
	for _, m := range combinedPlaceholderRe.FindAllStringSubmatch(text, -1) {
		switch {
		case m[1] != "":
			paramRefs = append(paramRefs, m[1])
		case m[2] != "":
			stateRefs = append(stateRefs, m[2])
		case m[3] != "":
			// prev ref: neither a task edge nor a param reference.
		case m[5] != "":
			taskRefs = append(taskRefs, m[5])
		default:
			taskRefs = append(taskRefs, m[4])
		}
	}
	return taskRefs, paramRefs, stateRefs
}

// brokenBraceRe matches any {{...}} token whose body contains no closing
// braces.
var brokenBraceRe = regexp.MustCompile(`\{\{[^}]*\}\}`)

// checkMalformedParamPlaceholders scans prompt for any {{params.}}-shaped
// token that combinedPlaceholderRe rejects.
func checkMalformedParamPlaceholders(tid TaskID, prompt string) error {
	for _, tok := range brokenBraceRe.FindAllString(prompt, -1) {
		if combinedPlaceholderRe.MatchString(tok) {
			continue
		}
		inner := strings.TrimSpace(tok[2 : len(tok)-2])
		if strings.HasPrefix(inner, "params.") {
			return &MalformedParamPlaceholderError{Task: tid, Token: tok}
		}
	}
	return nil
}

// validateSystemPrompt rejects task-id placeholders in the workflow-level
// system_prompt and rejects unknown / malformed param placeholders there too.
func validateSystemPrompt(sp string, params map[ParamName]struct{}) error {
	if sp == "" {
		return nil
	}
	taskRefs, paramRefs, _ := scanPlaceholders(sp)
	if len(taskRefs) > 0 {
		return &SystemPlaceholderTaskRefError{Name: taskRefs[0]}
	}
	for _, name := range paramRefs {
		if _, ok := params[ParamName(name)]; !ok {
			return &UnknownParamError{Task: "", Name: name}
		}
	}
	if err := checkMalformedParamPlaceholders("", sp); err != nil {
		return err
	}
	return nil
}

// checkUnusedParams enforces that every declared param is referenced by at
// least one prompt or by the system_prompt.
func checkUnusedParams(wf *Workflow) error {
	if len(wf.Params) == 0 {
		return nil
	}
	used := make(map[ParamName]struct{}, len(wf.Params))
	scan := func(s string) {
		_, paramRefs, _ := scanPlaceholders(s)
		for _, name := range paramRefs {
			used[ParamName(name)] = struct{}{}
		}
	}
	scan(wf.SystemPrompt)
	for i := range wf.Tasks {
		for _, body := range wf.Tasks[i].TextBodies() {
			scan(body)
		}
		scan(wf.Tasks[i].SystemPrompt)
	}
	for _, p := range wf.Params {
		if _, ok := used[p.Name]; !ok {
			return &UnusedParamError{Name: p.Name}
		}
	}
	return nil
}

// checkPrevPlaceholders enforces that every `{{prev.id}}` placeholder appears
// only inside a loop body task and references a member of that same loop. A
// prev reference names the prior iteration's output of a sibling member, so it
// is meaningless outside a loop and may never cross a loop boundary. It is also
// rejected inside a parallel for_each body: its passes run concurrently with no
// ordering, so there is no prior iteration to read.
func checkPrevPlaceholders(wf *Workflow, memberByLoop map[LoopID]map[TaskID]bool) error {
	parallelLoop := make(map[LoopID]bool, len(wf.Loops))
	for i := range wf.Loops {
		if wf.Loops[i].Parallel {
			parallelLoop[wf.Loops[i].ID] = true
		}
	}
	for i := range wf.Tasks {
		t := &wf.Tasks[i]
		for _, text := range t.TextBodies() {
			for _, m := range prevPlaceholderRe.FindAllStringSubmatch(text, -1) {
				name := m[1]
				if t.Loop == "" {
					return &PrevOutsideLoopError{Task: t.ID, Name: name}
				}
				if parallelLoop[t.Loop] {
					return &PrevInParallelLoopError{Task: t.ID, Loop: t.Loop, Name: name}
				}
				if !memberByLoop[t.Loop][TaskID(name)] {
					return &PrevNotMemberError{Task: t.ID, Loop: t.Loop, Name: name}
				}
			}
		}
	}
	return nil
}

// refScope bundles the context needed to classify and validate {{...}}
// placeholders in a task body or with-value.
type refScope struct {
	tid     TaskID
	known   map[TaskID]struct{}
	params  map[ParamName]struct{}
	loopVar string
}

// resolveRefs scans text for {{task}}, {{params.x}}, and related placeholders.
// In strict mode (implicit=false), a task ref not already in seen is an error.
// In implicit mode (implicit=true), a task ref not in seen but present in
// rs.known is added as an implicit dep edge; a ref not in rs.known is still an
// error.
//
// seen is updated with any new edges added (so the caller can union further
// scans without re-scanning). deps receives any new edges in order.
func (rs refScope) resolveRefs(text string, implicit bool, seen map[TaskID]struct{}, deps *[]TaskID) error {
	taskRefs, paramRefs, _ := scanPlaceholders(text)
	for _, name := range taskRefs {
		if name == rs.loopVar {
			continue
		}
		id := TaskID(name)
		if _, inSeen := seen[id]; inSeen {
			continue
		}
		if !implicit {
			err := &UnknownPlaceholderError{Task: rs.tid, Name: name}
			if _, isParam := rs.params[ParamName(name)]; isParam {
				err.Hint = fmt.Sprintf("did you mean {{params.%s}}?", name)
			}
			return err
		}
		// implicit: add if known, error if not
		if _, ok := rs.known[id]; !ok {
			return &UnknownPlaceholderError{Task: rs.tid, Name: name}
		}
		seen[id] = struct{}{}
		*deps = append(*deps, id)
	}
	for _, name := range paramRefs {
		if _, ok := rs.params[ParamName(name)]; !ok {
			return &UnknownParamError{Task: rs.tid, Name: name}
		}
	}
	return nil
}
