package workflow

import (
	"fmt"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/syntax"
)

// taskDecl is the semantic task declaration lowered from syntax.DraftTask. It
// keeps YAML decoding details at the parser boundary while later task-building
// code works with validated identifiers and workflow-owned field types.
type taskDecl struct {
	id               TaskID
	loop             LoopID
	description      string
	runtime          runtime.Name
	model            runtime.Model
	effort           runtime.Effort
	body             taskBodyDecl
	systemPrompt     string
	systemPromptFile string
	okExit           []int
	dependsOn        []string
	writesState      string
	when             string
	retry            Retry
	budget           *Budget
	schema           *Schema
	cache            *bool
}

func decodeTaskDecls(node syntax.Value, loop LoopID) ([]taskDecl, error) {
	var draftTasks []syntax.DraftTask
	if err := node.Decode(&draftTasks); err != nil {
		return nil, err
	}
	decls := make([]taskDecl, 0, len(draftTasks))
	for _, rt := range draftTasks {
		decl, err := newTaskDecl(rt, loop)
		if err != nil {
			return nil, err
		}
		decls = append(decls, decl)
	}
	return decls, nil
}

func newTaskDecl(rt syntax.DraftTask, loop LoopID) (taskDecl, error) {
	id, err := NewTaskID(rt.ID)
	if err != nil {
		return taskDecl{}, err
	}
	body, err := newTaskBodyDecl(id, rt)
	if err != nil {
		return taskDecl{}, err
	}
	retry, err := parseRetry(id, rt.Retry)
	if err != nil {
		return taskDecl{}, err
	}
	budget, err := parseBudget(rt.Budget)
	if err != nil {
		return taskDecl{}, fmt.Errorf("task %q: %w", id, err)
	}
	schema, err := parseSchema(rt.Schema)
	if err != nil {
		return taskDecl{}, fmt.Errorf("task %q: %w", id, err)
	}
	return taskDecl{
		id:               id,
		loop:             loop,
		description:      rt.Description,
		runtime:          runtime.Name(rt.Runtime),
		model:            runtime.Model(rt.Model),
		effort:           runtime.Effort(rt.Effort),
		body:             body,
		systemPrompt:     rt.SystemPrompt,
		systemPromptFile: rt.SystemPromptFile,
		okExit:           append([]int(nil), rt.OkExit...),
		dependsOn:        append([]string(nil), rt.DependsOn...),
		writesState:      rt.WritesState,
		when:             rt.When,
		retry:            retry,
		budget:           budget,
		schema:           schema,
		cache:            rt.Cache,
	}, nil
}
