// Package plan compiles checked workflows into executable graph shape.
package plan

import (
	"fmt"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// BlockID identifies a scoped block.
type BlockID string

// StepID identifies one executable step.
type StepID string

// Plan is the executable graph compiled from a Workflow.
type Plan struct {
	Root   BlockID
	Blocks map[BlockID]Block
	Steps  map[StepID]Step
	Order  []StepID
}

// Block groups steps under one scope.
type Block struct {
	ID     BlockID
	Steps  []StepID
	Output OutputExpr
}

// Step is one executable item.
type Step struct {
	ID     StepID
	Name   workflow.TaskID
	Deps   []StepID
	When   *workflow.Condition
	Action Action
	Policy Policy
}

// OutputExpr selects a block result.
type OutputExpr struct {
	Task workflow.TaskID
}

// Policy gathers execution policies for a step.
type Policy struct {
	Retry  workflow.Retry
	Cache  *bool
	Budget *workflow.Budget
	OkExit []int
}

// Action is the executable behavior of a step.
type Action interface {
	action()
}

// AskModel invokes a model runtime.
type AskModel struct {
	Prompt       workflow.Template
	SystemPrompt workflow.Template
	Runtime      runtime.Name
	Model        runtime.Model
	Effort       runtime.Effort
	Schema       *workflow.Schema
}

// RunCommand invokes the shell.
type RunCommand struct {
	Command workflow.Template
}

// RunScript invokes a script file.
type RunScript struct {
	Path workflow.Template
	Args []workflow.Template
}

// CallWorkflow invokes a linked workflow.
type CallWorkflow struct {
	Ref           workflow.WorkflowRef
	With          []workflow.WithArg
	WithTemplates []workflow.WithTemplate
}

// ForEach runs a block once per item.
type ForEach struct {
	Loop        workflow.LoopGroup
	Body        BlockID
	Concurrency int
}

// Repeat runs a block until convergence.
type Repeat struct {
	Loop workflow.LoopGroup
	Body BlockID
}

// InvalidAction marks an invalid hand-built task body.
type InvalidAction struct{}

func (AskModel) action()      {}
func (RunCommand) action()    {}
func (RunScript) action()     {}
func (CallWorkflow) action()  {}
func (ForEach) action()       {}
func (Repeat) action()        {}
func (InvalidAction) action() {}

// CompileOptions configures compilation.
type CompileOptions struct {
	Params  workflow.ParamValues
	Catalog runtime.Validator
}

// Compile builds a Plan from wf.
func Compile(wf *workflow.Workflow, opts CompileOptions) (*Plan, error) {
	if wf == nil {
		return nil, fmt.Errorf("workflow is nil")
	}
	root := BlockID("root")
	pl := &Plan{
		Root:   root,
		Blocks: map[BlockID]Block{root: {ID: root}},
		Steps:  make(map[StepID]Step, len(wf.Tasks)+len(wf.Loops)),
	}
	for _, id := range wf.Plan() {
		pl.Order = append(pl.Order, StepID(id))
	}

	memberOf := loopMembers(wf.Loops)
	for i := range wf.Tasks {
		step, err := compileTask(wf, &wf.Tasks[i])
		if err != nil {
			return nil, err
		}
		pl.Steps[step.ID] = step
	}
	rootBlock := pl.Blocks[root]
	for i := range wf.Loops {
		lg := wf.Loops[i]
		bodyID := BlockID(lg.ID)
		body := Block{ID: bodyID, Steps: stepIDs(lg.Members)}
		pl.Blocks[bodyID] = body

		step := Step{
			ID:     StepID(lg.ID),
			Name:   workflow.TaskID(lg.ID),
			Deps:   loopEntryDeps(wf, lg),
			Action: loopAction(lg, bodyID),
		}
		pl.Steps[step.ID] = step
		rootBlock.Steps = append(rootBlock.Steps, step.ID)
	}
	for _, id := range pl.Order {
		if _, ok := memberOf[workflow.TaskID(id)]; ok {
			continue
		}
		rootBlock.Steps = append(rootBlock.Steps, id)
	}
	pl.Blocks[root] = rootBlock
	return pl, nil
}

func compileTask(wf *workflow.Workflow, t *workflow.Task) (Step, error) {
	action, err := compileAction(wf, t)
	if err != nil {
		return Step{}, err
	}
	return Step{
		ID:     StepID(t.ID),
		Name:   t.ID,
		Deps:   stepIDs(t.DependsOn),
		When:   t.Cond,
		Action: action,
		Policy: Policy{
			Retry:  t.Retry,
			Cache:  t.Cache,
			Budget: t.Budget,
			OkExit: append([]int(nil), t.OkExit...),
		},
	}, nil
}

func compileAction(wf *workflow.Workflow, t *workflow.Task) (Action, error) {
	switch action := t.Action().(type) {
	case workflow.PromptAction:
		rt, model, effort := wf.Effective(t)
		return AskModel{
			Prompt:       action.Prompt,
			SystemPrompt: wf.EffectiveSystemPromptTemplate(t),
			Runtime:      rt,
			Model:        model,
			Effort:       effort,
			Schema:       t.Schema,
		}, nil
	case workflow.CommandAction:
		return RunCommand{Command: action.Command}, nil
	case workflow.ScriptAction:
		return RunScript{Path: action.Path, Args: append([]workflow.Template(nil), action.Args...)}, nil
	case workflow.WorkflowAction:
		return CallWorkflow{
			Ref:           action.Ref,
			With:          append([]workflow.WithArg(nil), action.With...),
			WithTemplates: append([]workflow.WithTemplate(nil), action.WithTemplates...),
		}, nil
	default:
		return InvalidAction{}, nil
	}
}

func loopAction(lg workflow.LoopGroup, body BlockID) Action {
	if lg.Kind == workflow.LoopForEach {
		concurrency := 1
		if lg.Parallel {
			concurrency = 0
		}
		return ForEach{Loop: lg, Body: body, Concurrency: concurrency}
	}
	return Repeat{Loop: lg, Body: body}
}

func stepIDs(ids []workflow.TaskID) []StepID {
	out := make([]StepID, len(ids))
	for i, id := range ids {
		out[i] = StepID(id)
	}
	return out
}

func loopMembers(loops []workflow.LoopGroup) map[workflow.TaskID]struct{} {
	out := make(map[workflow.TaskID]struct{})
	for _, loop := range loops {
		for _, member := range loop.Members {
			out[member] = struct{}{}
		}
	}
	return out
}

func loopEntryDeps(wf *workflow.Workflow, lg workflow.LoopGroup) []StepID {
	memberSet := make(map[workflow.TaskID]bool, len(lg.Members))
	for _, member := range lg.Members {
		memberSet[member] = true
	}
	seen := make(map[workflow.TaskID]bool)
	var deps []workflow.TaskID
	add := func(id workflow.TaskID) {
		if memberSet[id] || seen[id] {
			return
		}
		seen[id] = true
		deps = append(deps, id)
	}
	for _, member := range lg.Members {
		if task := wf.ByID(member); task != nil {
			for _, dep := range task.DependsOn {
				add(dep)
			}
		}
	}
	if lg.Kind == workflow.LoopForEach {
		if ref, ok := workflow.ListSourceTaskRef(lg.ListSource); ok {
			add(ref)
		}
	}
	return stepIDs(deps)
}
