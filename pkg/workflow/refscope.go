package workflow

import (
	"fmt"
	"regexp"
	"strings"
)

// scanPlaceholders walks text in a SINGLE pass with combinedPlaceholderRe and
// returns the task-id, param, and state placeholder names in source order.
func scanPlaceholders(text string) (taskRefs, paramRefs, stateRefs []string) {
	for _, ref := range ParseTemplate(text).Refs() {
		switch r := ref.(type) {
		case ParamRef:
			paramRefs = append(paramRefs, string(r.Name))
		case StateRef:
			stateRefs = append(stateRefs, r.Key)
		case TaskOutputRef:
			taskRefs = append(taskRefs, string(r.ID))
		case TaskExitRef:
			taskRefs = append(taskRefs, string(r.ID))
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

func validateRoutingField(tid TaskID, field, value string, params map[ParamName]struct{}) error {
	if value == "" || !strings.Contains(value, "{{") {
		return nil
	}
	name, ok := wholeParamPlaceholder(value)
	if !ok {
		return fmt.Errorf("%s must be a literal or exactly {{params.name}}", field)
	}
	if _, found := params[name]; !found {
		return &UnknownParamError{Task: tid, Name: string(name)}
	}
	return nil
}

// checkUnusedParamsDefinition enforces that every declared param is referenced
// by at least one prompt, routing field, or system_prompt.
func checkUnusedParamsDefinition(def WorkflowDefinition) error {
	if len(def.Params) == 0 {
		return nil
	}
	used := make(map[ParamName]struct{}, len(def.Params))
	scan := func(s string) {
		for _, ref := range ParseTemplate(s).Refs() {
			param, ok := ref.(ParamRef)
			if ok {
				used[param.Name] = struct{}{}
			}
		}
	}
	scan(def.Defaults.SystemPrompt.String())
	scan(string(def.Defaults.Runtime))
	scan(string(def.Defaults.Model))
	scan(string(def.Defaults.Effort))
	for _, task := range definitionTaskNodes(def) {
		for _, body := range taskNodeTextBodies(task) {
			scan(body)
		}
		scan(task.SystemPrompt.String())
		scan(string(task.Runtime.Runtime))
		scan(string(task.Runtime.Model))
		scan(string(task.Runtime.Effort))
	}
	for _, p := range def.Params {
		if _, ok := used[p.Name]; !ok {
			return &UnusedParamError{Name: p.Name}
		}
	}
	return nil
}

func taskNodeTextBodies(task TaskNode) []string {
	bodies := make([]string, 0)
	add := func(text string) {
		if text != "" {
			bodies = append(bodies, text)
		}
	}
	switch action := task.Action.(type) {
	case PromptAction:
		add(action.Prompt.String())
	case CommandAction:
		add(action.Command.String())
	case ScriptAction:
		add(action.Path.String())
		for _, arg := range action.Args {
			add(arg.String())
		}
	case SubWorkflowAction:
		for _, arg := range action.WithTemplates {
			add(arg.Value.String())
		}
	}
	return bodies
}

// checkPrevPlaceholdersDefinition enforces that every `{{prev.id}}`
// placeholder appears only inside a loop body task and references a member of
// that same loop. A prev reference names the prior iteration's output of a
// sibling member, so it is meaningless outside a loop and may never cross a loop
// boundary. It is also rejected inside a parallel for_each body: its passes run
// concurrently with no ordering, so there is no prior iteration to read.
func checkPrevPlaceholdersDefinition(def WorkflowDefinition, memberByLoop map[LoopID]map[TaskID]bool) error {
	parallelLoop := make(map[LoopID]bool)
	for _, node := range def.Nodes {
		loop, ok := node.(LoopNode)
		if ok && loop.Spec.Parallel {
			parallelLoop[loop.ID] = true
		}
	}
	for _, task := range definitionTaskNodes(def) {
		for _, text := range taskNodeTextBodies(task) {
			for _, ref := range ParseTemplate(text).Refs() {
				prev, ok := ref.(PrevRef)
				if !ok {
					continue
				}
				name := string(prev.ID)
				if task.Loop == "" {
					return &PrevOutsideLoopError{Task: task.ID, Name: name}
				}
				if parallelLoop[task.Loop] {
					return &PrevInParallelLoopError{Task: task.ID, Loop: task.Loop, Name: name}
				}
				if !memberByLoop[task.Loop][prev.ID] {
					return &PrevNotMemberError{Task: task.ID, Loop: task.Loop, Name: name}
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
	for _, ref := range ParseTemplateInScope(text, rs.loopVar).Refs() {
		switch r := ref.(type) {
		case LoopVarRef:
			continue
		case TaskOutputRef:
			if err := rs.resolveTaskRef(string(r.ID), implicit, seen, deps); err != nil {
				return err
			}
		case TaskExitRef:
			if err := rs.resolveTaskRef(string(r.ID), implicit, seen, deps); err != nil {
				return err
			}
		case ParamRef:
			if _, ok := rs.params[r.Name]; !ok {
				return &UnknownParamError{Task: rs.tid, Name: string(r.Name)}
			}
		}
	}
	return nil
}

func (rs refScope) resolveTaskRef(name string, implicit bool, seen map[TaskID]struct{}, deps *[]TaskID) error {
	id := TaskID(name)
	if _, inSeen := seen[id]; inSeen {
		return nil
	}
	if !implicit {
		err := &UnknownPlaceholderError{Task: rs.tid, Name: name}
		if _, isParam := rs.params[ParamName(name)]; isParam {
			err.Hint = fmt.Sprintf("did you mean {{params.%s}}?", name)
		}
		return err
	}
	if _, ok := rs.known[id]; !ok {
		return &UnknownPlaceholderError{Task: rs.tid, Name: name}
	}
	seen[id] = struct{}{}
	*deps = append(*deps, id)
	return nil
}
