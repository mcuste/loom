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

// Program is the compiled interpreter IR for a workflow.
type Program = Plan

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

// ExecutableUnit is the architecture-level name for one compiled unit.
type ExecutableUnit = Step

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
	Schema *workflow.Schema
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
	return CompileDefinition(wf.Definition(), opts)
}

// CompileDefinition builds a Plan from the parsed workflow definition. The
// compiler consumes the node/action model rather than YAML-shaped task fields,
// keeping execution planning separate from manifest decoding details.
func CompileDefinition(def workflow.WorkflowDefinition, _ CompileOptions) (*Plan, error) {
	tasks, loops, taskByID := definitionNodes(def)
	root := BlockID("root")
	pl := &Plan{
		Root:   root,
		Blocks: map[BlockID]Block{root: {ID: root}},
		Steps:  make(map[StepID]Step, len(tasks)+len(loops)),
	}
	for _, id := range def.Order {
		pl.Order = append(pl.Order, StepID(id))
	}
	if len(pl.Order) == 0 {
		for _, task := range tasks {
			pl.Order = append(pl.Order, StepID(task.ID))
		}
	}

	memberOf := loopMembers(loops)
	for _, task := range tasks {
		step, err := compileTask(def, task)
		if err != nil {
			return nil, err
		}
		pl.Steps[step.ID] = step
	}
	rootBlock := pl.Blocks[root]
	for _, loop := range loops {
		lg := loop.Spec
		bodyID := BlockID(lg.ID)
		body := Block{ID: bodyID, Steps: stepIDs(lg.Members)}
		pl.Blocks[bodyID] = body

		step := Step{
			ID:     StepID(lg.ID),
			Name:   workflow.TaskID(lg.ID),
			Deps:   loopEntryDeps(taskByID, lg),
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

func definitionNodes(def workflow.WorkflowDefinition) ([]workflow.TaskNode, []workflow.LoopNode, map[workflow.TaskID]workflow.TaskNode) {
	var tasks []workflow.TaskNode
	var loops []workflow.LoopNode
	taskByID := make(map[workflow.TaskID]workflow.TaskNode)
	addTask := func(task workflow.TaskNode) {
		tasks = append(tasks, task)
		taskByID[task.ID] = task
	}
	for _, node := range def.Nodes {
		switch n := node.(type) {
		case workflow.TaskNode:
			addTask(n)
		case workflow.LoopNode:
			loops = append(loops, n)
			for _, task := range n.Body.Nodes {
				addTask(task)
			}
		}
	}
	return tasks, loops, taskByID
}

func compileTask(def workflow.WorkflowDefinition, t workflow.TaskNode) (Step, error) {
	action, err := compileAction(def, t)
	if err != nil {
		return Step{}, err
	}
	return Step{
		ID:     StepID(t.ID),
		Name:   t.ID,
		Deps:   nodeStepIDs(t.DependsOn),
		When:   t.Condition,
		Action: action,
		Policy: Policy{
			Retry:  t.Policies.Retry,
			Cache:  t.Policies.Cache,
			Budget: t.Policies.Budget,
			Schema: t.Policies.Schema,
			OkExit: append([]int(nil), t.Policies.OkExit...),
		},
	}, nil
}

func compileAction(def workflow.WorkflowDefinition, t workflow.TaskNode) (Action, error) {
	switch action := t.Action.(type) {
	case workflow.PromptAction:
		rt, model, effort := effectiveRuntime(def.Defaults, t.Runtime)
		systemPrompt := effectiveSystemPrompt(def.Defaults.SystemPrompt, t.SystemPrompt)
		return AskModel{
			Prompt:       action.Prompt,
			SystemPrompt: systemPrompt,
			Runtime:      rt,
			Model:        model,
			Effort:       effort,
			Schema:       t.Policies.Schema,
		}, nil
	case workflow.CommandAction:
		return RunCommand{Command: action.Command}, nil
	case workflow.ScriptAction:
		return RunScript{Path: action.Path, Args: append([]workflow.Template(nil), action.Args...)}, nil
	case workflow.SubWorkflowAction:
		return CallWorkflow{
			Ref:           action.Ref,
			With:          append([]workflow.WithArg(nil), action.With...),
			WithTemplates: append([]workflow.WithTemplate(nil), action.WithTemplates...),
		}, nil
	default:
		return InvalidAction{}, nil
	}
}

func effectiveRuntime(defaults workflow.WorkflowDefaults, rt workflow.RuntimeSelector) (runtime.Name, runtime.Model, runtime.Effort) {
	r := rt.Runtime
	if r == "" {
		r = defaults.Runtime
	}
	m := rt.Model
	if m == "" {
		m = defaults.Model
	}
	e := rt.Effort
	if e == "" {
		e = defaults.Effort
	}
	return r, m, e
}

func effectiveSystemPrompt(defaultPrompt, taskPrompt workflow.Template) workflow.Template {
	if taskPrompt.String() != "" {
		return taskPrompt
	}
	return defaultPrompt
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

func nodeStepIDs(ids []workflow.NodeID) []StepID {
	out := make([]StepID, len(ids))
	for i, id := range ids {
		out[i] = StepID(id)
	}
	return out
}

func loopMembers(loops []workflow.LoopNode) map[workflow.TaskID]struct{} {
	out := make(map[workflow.TaskID]struct{})
	for _, loop := range loops {
		for _, member := range loop.Spec.Members {
			out[member] = struct{}{}
		}
	}
	return out
}

func loopEntryDeps(tasks map[workflow.TaskID]workflow.TaskNode, lg workflow.LoopGroup) []StepID {
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
		if task, ok := tasks[member]; ok {
			for _, dep := range task.DependsOn {
				add(workflow.TaskID(dep))
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
