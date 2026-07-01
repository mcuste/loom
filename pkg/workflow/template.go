package workflow

import (
	"strconv"
	"strings"
)

// Template is parsed text plus placeholder references.
type Template struct {
	raw    string
	parts  []TemplatePart
	parsed bool
}

// TemplatePart is one literal or reference segment.
type TemplatePart interface {
	templatePart()
}

// TextPart is literal template text.
type TextPart struct {
	Text string
}

// RefPart is one placeholder reference.
type RefPart struct {
	Ref Ref
}

func (TextPart) templatePart() {}
func (RefPart) templatePart()  {}

// Ref is one resolved placeholder reference kind.
type Ref interface {
	ref()
}

// TaskOutputRef reads another task's output.
type TaskOutputRef struct{ ID TaskID }

// TaskExitRef reads another task's exit code.
type TaskExitRef struct{ ID TaskID }

// ParamRef reads a workflow param.
type ParamRef struct{ Name ParamName }

// StateRef reads a persisted state key.
type StateRef struct{ Key string }

// PrevRef reads a prior loop iteration output.
type PrevRef struct{ ID TaskID }

// LoopVarRef reads the current loop variable binding.
type LoopVarRef struct{ Name string }

func (TaskOutputRef) ref() {}
func (TaskExitRef) ref()   {}
func (ParamRef) ref()      {}
func (StateRef) ref()      {}
func (PrevRef) ref()       {}
func (LoopVarRef) ref()    {}

// ParseTemplate parses placeholders in text.
func ParseTemplate(text string) Template {
	return ParseTemplateInScope(text, "")
}

// ParseTemplateInScope parses placeholders, treating the named loop variable
// as a LoopVarRef instead of a task output ref.
func ParseTemplateInScope(text, loopVar string) Template {
	matches := combinedPlaceholderRe.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return Template{
			raw:    text,
			parts:  []TemplatePart{TextPart{Text: text}},
			parsed: true,
		}
	}

	parts := make([]TemplatePart, 0, len(matches)*2+1)
	last := 0
	for _, m := range matches {
		if m[0] > last {
			parts = append(parts, TextPart{Text: text[last:m[0]]})
		}
		parts = append(parts, RefPart{Ref: refFromMatch(text, m, loopVar)})
		last = m[1]
	}
	if last < len(text) {
		parts = append(parts, TextPart{Text: text[last:]})
	}
	return Template{raw: text, parts: parts, parsed: true}
}

func refFromMatch(text string, m []int, loopVar string) Ref {
	switch {
	case m[2] >= 0:
		return ParamRef{Name: ParamName(text[m[2]:m[3]])}
	case m[4] >= 0:
		return StateRef{Key: text[m[4]:m[5]]}
	case m[6] >= 0:
		return PrevRef{ID: TaskID(text[m[6]:m[7]])}
	case m[10] >= 0:
		return TaskExitRef{ID: TaskID(text[m[10]:m[11]])}
	default:
		name := text[m[8]:m[9]]
		if loopVar != "" && name == loopVar {
			return LoopVarRef{Name: name}
		}
		return TaskOutputRef{ID: TaskID(name)}
	}
}

// String returns the original text.
func (t Template) String() string {
	return t.raw
}

// Parts returns a copy of the parsed template parts.
func (t Template) Parts() []TemplatePart {
	return append([]TemplatePart(nil), t.parts...)
}

// Refs returns placeholder refs in source order.
func (t Template) Refs() []Ref {
	refs := make([]Ref, 0, len(t.parts))
	for _, part := range t.parts {
		if rp, ok := part.(RefPart); ok {
			refs = append(refs, rp.Ref)
		}
	}
	return refs
}

// RenderContext supplies values for template rendering.
type RenderContext struct {
	Outputs   map[TaskID]string
	Params    ParamValues
	State     map[string]string
	Prev      map[TaskID]string
	ExitCodes map[TaskID]int
	LoopVars  map[string]string
}

// Render substitutes every reference in one pass.
func (t Template) Render(ctx RenderContext) string {
	if !t.parsed {
		return t.raw
	}
	var b strings.Builder
	b.Grow(len(t.raw))
	for _, part := range t.parts {
		switch p := part.(type) {
		case TextPart:
			b.WriteString(p.Text)
		case RefPart:
			b.WriteString(renderRef(p.Ref, ctx))
		}
	}
	return b.String()
}

func renderRef(ref Ref, ctx RenderContext) string {
	switch r := ref.(type) {
	case ParamRef:
		if v, ok := ctx.Params[r.Name]; ok {
			return v
		}
		return rawRef(ref)
	case StateRef:
		return ctx.State[r.Key]
	case PrevRef:
		return ctx.Prev[r.ID]
	case TaskExitRef:
		if v, ok := ctx.ExitCodes[r.ID]; ok {
			return strconv.Itoa(v)
		}
		return rawRef(ref)
	case TaskOutputRef:
		if v, ok := ctx.Outputs[r.ID]; ok {
			return v
		}
		return rawRef(ref)
	case LoopVarRef:
		return ctx.LoopVars[r.Name]
	default:
		return rawRef(ref)
	}
}

func rawRef(ref Ref) string {
	switch r := ref.(type) {
	case ParamRef:
		return "{{params." + string(r.Name) + "}}"
	case StateRef:
		return "{{state." + r.Key + "}}"
	case PrevRef:
		return "{{prev." + string(r.ID) + "}}"
	case TaskExitRef:
		return "{{" + string(r.ID) + ".exit}}"
	case TaskOutputRef:
		return "{{" + string(r.ID) + "}}"
	case LoopVarRef:
		return "{{" + r.Name + "}}"
	default:
		return ""
	}
}
