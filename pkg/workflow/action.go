package workflow

// WorkflowRef names a linked child workflow.
type WorkflowRef string

// Action is the typed body of a task.
type Action interface {
	action()
}

// PromptAction sends a prompt to a model runtime.
type PromptAction struct {
	Prompt Template
}

// CommandAction runs a shell command.
type CommandAction struct {
	Command Template
}

// ScriptAction runs a script path with argv.
type ScriptAction struct {
	Path Template
	Args []Template
}

// SubWorkflowAction calls a linked child workflow.
type SubWorkflowAction struct {
	Ref           WorkflowRef
	With          []WithArg
	WithTemplates []WithTemplate
}

// WithTemplate is one parsed with-value.
type WithTemplate struct {
	Name  ParamName
	Value Template
}

func (PromptAction) action()      {}
func (CommandAction) action()     {}
func (ScriptAction) action()      {}
func (SubWorkflowAction) action() {}

// Action returns the task body action. Parsed tasks carry this value directly;
// hand-built tasks are normalized from their materialized YAML fields on demand.
func (t Task) Action() Action {
	if t.action != nil {
		return t.action
	}
	switch bodyKindFromFields(t) {
	case BodyPrompt:
		return PromptAction{Prompt: ParseTemplate(t.Prompt)}
	case BodyShell:
		return CommandAction{Command: ParseTemplate(t.Command)}
	case BodyScript:
		return ScriptAction{Path: ParseTemplate(t.Script), Args: parseTemplates(t.Args, "")}
	case BodySubWorkflow:
		return SubWorkflowAction{
			Ref:           WorkflowRef(t.Workflow),
			With:          append([]WithArg(nil), t.With...),
			WithTemplates: parseWithTemplates(t.With, ""),
		}
	default:
		return nil
	}
}

// ParsedAction returns the parse-built action, when this Task came from Parse.
func (t Task) ParsedAction() (Action, bool) {
	if t.action == nil {
		return nil, false
	}
	return t.action, true
}

func taskActionFromRaw(rt rawTask, withArgs []WithArg, loopVar string) Action {
	switch {
	case rt.Prompt != "":
		return PromptAction{Prompt: ParseTemplateInScope(rt.Prompt, loopVar)}
	case rt.Command != "":
		return CommandAction{Command: ParseTemplateInScope(rt.Command, loopVar)}
	case rt.Script != "":
		return ScriptAction{
			Path: ParseTemplateInScope(rt.Script, loopVar),
			Args: parseTemplates(rt.Args, loopVar),
		}
	case rt.Workflow != "":
		return SubWorkflowAction{
			Ref:           WorkflowRef(rt.Workflow),
			With:          append([]WithArg(nil), withArgs...),
			WithTemplates: parseWithTemplates(withArgs, loopVar),
		}
	default:
		return nil
	}
}

func parseTemplates(values []string, loopVar string) []Template {
	if len(values) == 0 {
		return nil
	}
	out := make([]Template, len(values))
	for i, value := range values {
		out[i] = ParseTemplateInScope(value, loopVar)
	}
	return out
}

func parseWithTemplates(args []WithArg, loopVar string) []WithTemplate {
	if len(args) == 0 {
		return nil
	}
	out := make([]WithTemplate, len(args))
	for i, arg := range args {
		out[i] = WithTemplate{
			Name:  arg.Name,
			Value: ParseTemplateInScope(arg.Value, loopVar),
		}
	}
	return out
}

// Template returns the parsed value template.
func (a WithArg) Template() Template {
	return ParseTemplate(a.Value)
}

func bodyKindForAction(action Action) BodyKind {
	switch action.(type) {
	case PromptAction:
		return BodyPrompt
	case CommandAction:
		return BodyShell
	case ScriptAction:
		return BodyScript
	case SubWorkflowAction:
		return BodySubWorkflow
	default:
		return BodyInvalid
	}
}
