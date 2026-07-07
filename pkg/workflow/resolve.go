package workflow

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/syntax"
)

// ParamValues is a resolved-param bag: every entry is the final string value
// (after CLI / file / default merge) for a declared param. Callers that build
// a Workflow by hand may pass nil.
type ParamValues map[ParamName]string

// Effective returns the runtime, model, and effort the registered runtime will
// see for t. Task-level fields win when non-empty, falling back to the
// workflow-level defaults. The system prompt follows the same rule via the
// separate [Workflow.EffectiveSystemPrompt].
//
// t must be non-nil.
func (w *Workflow) Effective(t *Task) (runtime.Name, runtime.Model, runtime.Effort) {
	r := t.Runtime
	if r == "" {
		r = w.Runtime
	}
	m := t.Model
	if m == "" {
		m = w.Model
	}
	e := t.Effort
	if e == "" {
		e = w.Effort
	}
	return r, m, e
}

// EffectiveWithParams is Effective plus whole-field param resolution for
// runtime/model/effort. A routing field may be either a literal value or exactly
// `{{params.name}}`; partial templates are rejected by Parse.
func (w *Workflow) EffectiveWithParams(t *Task, params ParamValues) (runtime.Name, runtime.Model, runtime.Effort) {
	r, m, e := w.Effective(t)
	return runtime.Name(resolveRoutingValue(string(r), params)),
		runtime.Model(resolveRoutingValue(string(m), params)),
		runtime.Effort(resolveRoutingValue(string(e), params))
}

func resolveRoutingValue(s string, params ParamValues) string {
	if name, ok := wholeParamPlaceholder(s); ok {
		if v, found := params[name]; found {
			return v
		}
	}
	return s
}

func wholeParamPlaceholder(s string) (ParamName, bool) {
	m := paramPlaceholderRe.FindStringSubmatch(s)
	if len(m) != 2 || m[0] != s {
		return "", false
	}
	return ParamName(m[1]), true
}

// PlaceholderParamName reports whether s is exactly a `{{params.name}}`
// placeholder and, when it is, returns the captured param name.
func PlaceholderParamName(s string) (string, bool) {
	name, ok := wholeParamPlaceholder(s)
	return string(name), ok
}

// StartMeta returns the runtime, model, and effort a task reports to a hook's
// OnStart when it begins. A prompt (LLM) task carries its [Workflow.Effective]
// triple; shell, script, and sub-workflow tasks have no runtime of their own (a
// sub-workflow's child brings its own) and report the empty triple, matching the
// per-kind OnStart calls the executor's dispatch makes. It is the single
// authority for "which task kinds carry runtime metadata", so seed stamping can
// mirror a fresh run without re-deriving the rule.
//
// t must be non-nil.
func (w *Workflow) StartMeta(t *Task) (runtime.Name, runtime.Model, runtime.Effort) {
	if t.BodyKind() == BodyPrompt {
		return w.Effective(t)
	}
	return "", "", ""
}

// EffectiveSystemPrompt returns the system prompt the runtime will see for t:
// the task-level SystemPrompt when non-empty, otherwise the workflow-level
// default. It mirrors [Workflow.Effective]'s task-over-workflow fallback so a
// task can specialize its system prompt without disturbing the rest of the
// workflow. The result still carries unresolved `{{params.x}}` / `{{state.k}}`
// placeholders; the caller substitutes them before dispatch.
//
// t must be non-nil.
func (w *Workflow) EffectiveSystemPrompt(t *Task) string {
	if t.SystemPrompt != "" {
		return t.SystemPrompt
	}
	return w.SystemPrompt
}

// EffectiveSystemPromptTemplate returns the parsed effective system prompt.
func (w *Workflow) EffectiveSystemPromptTemplate(t *Task) Template {
	if t.SystemPrompt != "" {
		if t.systemPromptTemplate.parsed {
			return t.systemPromptTemplate
		}
		return ParseTemplate(t.SystemPrompt)
	}
	if w.systemPromptTemplate.parsed {
		return w.systemPromptTemplate
	}
	return ParseTemplate(w.SystemPrompt)
}

// CacheEnabled reports whether t opts into output memoization. The task's own
// `cache:` override wins when set (*true opts in, *false opts out); a nil
// override inherits the workflow-level Cache default. Shell-ness is not checked
// here: the executor never memoizes shell tasks regardless.
//
// t must be non-nil.
func (w *Workflow) CacheEnabled(t *Task) bool {
	if t.Cache != nil {
		return *t.Cache
	}
	return w.Cache
}

// Substitute replaces every `{{id}}`, `{{params.name}}`, `{{state.key}}`,
// `{{prev.id}}`, and `{{id.exit}}` placeholder in prompt with outputs[id] /
// params[name] / state[key] / prev[id] / exitCodes[id] respectively, in a single
// pass. An `{{id.exit}}` placeholder substitutes the referenced task's process
// exit code as a decimal integer (0 for any task that is not a script task).
//
// The single-pass guarantee matters: substituting one namespace before another
// would re-expand a value that happened to contain placeholder text. By
// splicing all kinds in one scan, a param value of `{{a}}` survives as the
// literal string `{{a}}` even if task `a` produced an output, and the namespaces
// never collide: `{{draft}}`, `{{params.draft}}`, and `{{prev.draft}}` each
// resolve from their own map.
//
// Any map may be nil. An unknown task, exit, or param placeholder is left in
// place verbatim, mirroring the parser-time invariant that every such
// placeholder in a Workflow returned by Parse is guaranteed to resolve. A
// missing state key or prev id, by contrast, substitutes to the empty string:
// both are legitimately empty on the first tick (state across runs, prev on the
// first loop iteration), so the placeholder must collapse rather than leak
// braces.
func Substitute(prompt string, outputs map[TaskID]string, params ParamValues, state map[string]string, prev map[TaskID]string, exitCodes map[TaskID]int) string {
	return ParseTemplate(prompt).Render(RenderContext{
		Outputs:   outputs,
		Params:    params,
		State:     state,
		Prev:      prev,
		ExitCodes: exitCodes,
	})
}

// ResolveParams merges declared defaults, file-supplied values, and CLI
// values (CLI wins, then file, then default) into a ParamValues bag.
//
// Every key in cli and file must name a declared param; otherwise the
// respective UnknownCLIParamError / UnknownFileParamError is returned. Every
// Required param must end up with a value; otherwise MissingRequiredParamError.
//
// Never mutates wf. Returns a fresh map sized to len(wf.Params).
func ResolveParams(wf *Workflow, cli, file map[string]string) (ParamValues, error) {
	declared := make(map[ParamName]struct{}, len(wf.Params))
	for _, p := range wf.Params {
		declared[p.Name] = struct{}{}
	}
	for k := range file {
		if _, ok := declared[ParamName(k)]; !ok {
			return nil, &UnknownFileParamError{Name: k}
		}
	}
	for k := range cli {
		if _, ok := declared[ParamName(k)]; !ok {
			return nil, &UnknownCLIParamError{Name: k}
		}
	}

	out := make(ParamValues, len(wf.Params))
	for _, p := range wf.Params {
		if p.HasDefault {
			out[p.Name] = p.Default
		}
	}
	for k, v := range file {
		out[ParamName(k)] = v
	}
	for k, v := range cli {
		out[ParamName(k)] = v
	}
	for _, p := range wf.Params {
		if _, ok := out[p.Name]; !ok {
			if p.Required {
				return nil, &MissingRequiredParamError{Name: p.Name, Partial: out}
			}
		}
	}
	return out, nil
}

// ParseParamArgs decodes a slice of `key=val` strings (as collected from
// repeated `-p` flags) into a map. The first `=` is the separator; subsequent
// `=` characters are kept in the value verbatim. Empty values (`key=`) are
// allowed and pass through as `""`.
//
// Duplicate keys are a hard error (DuplicateCLIParamError) rather than
// last-wins, matching the design's explicit duplicate rule.
func ParseParamArgs(args []string) (map[string]string, error) {
	if len(args) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(args))
	for _, arg := range args {
		key, val, ok := strings.Cut(arg, "=")
		if !ok {
			return nil, &MalformedParamArgError{Arg: arg, Reason: "expected key=val"}
		}
		if key == "" {
			return nil, &MalformedParamArgError{Arg: arg, Reason: "empty key"}
		}
		if !identifierRe.MatchString(key) {
			return nil, &MalformedParamArgError{Arg: arg, Reason: "key must match [A-Za-z0-9_]+"}
		}
		if !utf8.ValidString(val) || strings.ContainsRune(val, 0) {
			return nil, &InvalidParamValueError{Name: key}
		}
		if _, dup := out[key]; dup {
			return nil, &DuplicateCLIParamError{Name: key}
		}
		out[key] = val
	}
	return out, nil
}

// MissingRequiredParamError reports a required param that received no value
// from CLI, file, or default. Partial holds the param bag as it stood at the
// point of failure: defaults merged with file and CLI values, minus the absent
// required key. Callers that need a best-effort bag (e.g. advisory plan
// display) read Partial rather than rebuilding the merge themselves.
type MissingRequiredParamError struct {
	Name    ParamName
	Partial ParamValues
}

func (e *MissingRequiredParamError) Error() string {
	return fmt.Sprintf("param %q: required value not supplied", e.Name)
}

// UnknownCLIParamError reports a `-p key=val` whose key is not in the
// workflow's `params:` block.
type UnknownCLIParamError struct{ Name string }

func (e *UnknownCLIParamError) Error() string {
	return fmt.Sprintf("unknown param %q on command line", e.Name)
}

// UnknownFileParamError reports a `--params-file` entry whose key is not in
// the workflow's `params:` block. ResolveParams emits it today for any caller
// that supplies a file map.
type UnknownFileParamError struct{ Name string }

func (e *UnknownFileParamError) Error() string {
	return fmt.Sprintf("unknown param %q in params file", e.Name)
}

// MalformedParamArgError reports a `-p` value that doesn't satisfy the strict
// `key=val` shape (missing `=`, empty key, key not in identifier alphabet).
type MalformedParamArgError struct {
	Arg    string
	Reason string
}

func (e *MalformedParamArgError) Error() string {
	return fmt.Sprintf("malformed -p value %q: %s", e.Arg, e.Reason)
}

// DuplicateCLIParamError reports the same key passed via `-p` twice.
type DuplicateCLIParamError struct{ Name string }

func (e *DuplicateCLIParamError) Error() string {
	return fmt.Sprintf("param %q supplied more than once on command line", e.Name)
}

// InvalidParamValueError reports a CLI/file param value containing invalid
// UTF-8 or a NUL byte: values that would not survive a YAML round trip.
type InvalidParamValueError struct{ Name string }

func (e *InvalidParamValueError) Error() string {
	return fmt.Sprintf("param %q: value contains invalid UTF-8 or NUL", e.Name)
}

// parseParams validates the raw params: block and returns the resolved
// Params slice in declaration order plus an index from name to slice position.
func parseParams(node syntax.Value) ([]Param, map[ParamName]int, error) {
	if !node.Present() {
		return nil, nil, nil
	}
	if node.Kind() != syntax.SequenceNode {
		return nil, nil, fmt.Errorf("params: must be a sequence of param entries")
	}
	entries := node.SequenceValues()
	params := make([]Param, 0, len(entries))
	idx := make(map[ParamName]int, len(entries))
	for _, entry := range entries {
		rp, defNode, err := decodeDraftParam(entry)
		if err != nil {
			return nil, nil, err
		}
		if rp.Name == "" {
			return nil, nil, ErrMissingParamName
		}
		name, err := NewParamName(rp.Name)
		if err != nil {
			return nil, nil, err
		}
		if _, dup := idx[name]; dup {
			return nil, nil, &DuplicateParamNameError{Name: name}
		}
		p := Param{
			Name:        name,
			Description: rp.Description,
			Required:    rp.Required,
		}
		if defNode.Present() {
			if rp.Required {
				return nil, nil, &ConflictingParamSpecError{Name: name}
			}
			if defNode.Kind() != syntax.ScalarNode {
				return nil, nil, &InvalidParamDefaultError{Name: name, Reason: "must be a scalar string"}
			}
			if defNode.Tag() == "!!null" {
				return nil, nil, &InvalidParamDefaultError{Name: name, Reason: "null default is not allowed"}
			}
			p.Default = defNode.Scalar()
			p.HasDefault = true
		}
		idx[name] = len(params)
		params = append(params, p)
	}
	return params, idx, nil
}

// decodeDraftParam destructures a single params: mapping entry.
func decodeDraftParam(entry syntax.Value) (syntax.DraftParam, syntax.Value, error) {
	var rp syntax.DraftParam
	if entry.Kind() != syntax.MappingNode {
		return rp, syntax.Value{}, fmt.Errorf("params: entry must be a mapping")
	}
	var defNode syntax.Value
	if err := entry.EachMapEntry("params: entry", func(key string, v syntax.Value) error {
		switch key {
		case "name":
			if err := v.Decode(&rp.Name); err != nil {
				return fmt.Errorf("params.name: %w", err)
			}
		case "description":
			if err := v.Decode(&rp.Description); err != nil {
				return fmt.Errorf("params.description: %w", err)
			}
		case "required":
			if err := v.Decode(&rp.Required); err != nil {
				return fmt.Errorf("params.required: %w", err)
			}
		case "default":
			defNode = v
		default:
			return fmt.Errorf("params: unknown field %q", key)
		}
		return nil
	}); err != nil {
		return rp, syntax.Value{}, err
	}
	return rp, defNode, nil
}
