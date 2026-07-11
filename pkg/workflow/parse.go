// Package workflow defines the domain types for loom workflow definitions.
// See workflow.go for the core types and workflow.go's package doc.
//
// Parse decodes a workflow YAML document and returns a legacy Workflow view of
// the validated semantic WorkflowDefinition.
//
// The decoder runs in known-fields mode: any top-level or task-level key not
// recognized by the current schema is rejected. The sub-workflow constructs
// (top-level output:, task-level workflow: and with:) are recognized here;
// linking the child workflows referenced by workflow: is a separate CLI step
// so this package stays filesystem-free.
//
// The load-time pipeline is intentionally layered:
//
//  1. pkg/syntax decodes YAML into syntax.Document without assigning domain
//     meaning.
//  2. parseDeclarations validates identifiers and lowers raw syntax into
//     parser declarations, parsing local semantic blocks such as retry:,
//     budget:, schema:, schedule:, and with: into workflow-owned values.
//  3. analyzeDeclaration validates cross-declaration invariants such as task
//     uniqueness, loop namespaces, loop membership, dependency references,
//     conditions, and loop convergence.
//  4. lowerAnalyzedDefinition builds the semantic WorkflowDefinition from the
//     already-analyzed graph without reaching back into YAML syntax.
//  5. Parse materializes the legacy Workflow view from that definition for
//     existing callers; ParseDefinition exposes the semantic model directly.
//  6. Invocation-time checks that need resolved params, linked sub-workflows, or
//     a runtime catalog stay outside Parse in pkg/workflowcheck/pkg/workflowload.
//
// Validation performed by Parse includes:
//
//  1. Workflow name and every task id satisfy [A-Za-z0-9_]+.
//  2. Task ids are unique.
//  3. Param block: names are valid, unique, required-vs-default is exclusive,
//     defaults are scalar strings.
//  4. Every task sets exactly one body form. A shell/script task must not set
//     task-level runtime, model, effort, system_prompt, or schema.
//  5. Every depends_on entry names a known task and appears at most once.
//  6. Every {{id}} placeholder in a prompt, command, script, or with-value is a
//     member of that task's depends_on. Placeholders are pure templating; they
//     never extend the dependency graph implicitly.
//  7. Every {{params.x}} placeholder references a declared param.
//  8. Loop wrappers, loop namespaces, and loop convergence targets are valid.
//  9. The task graph has no cycles.
//  10. Every prompt, command, script, with-value, and system_prompt is free of
//     malformed {{params.}} tokens; a system_prompt is free of task-id
//     placeholders.
//  11. Every declared param is referenced by at least one prompt, command,
//     script, with-value, routing field, or system_prompt.
//
// Runtime-catalog validation is intentionally outside this parser; callers use
// pkg/workflowcheck after params are resolved and sub-workflows are linked.
package workflow

import (
	"fmt"

	"github.com/mcuste/loom/pkg/syntax"
)

// ParseOptions configures conversion from syntax draft to Workflow.
type ParseOptions struct {
	Source syntax.Source
}

type parser struct {
	doc *syntax.Document
	id  WorkflowID
}

// Parse decodes a workflow YAML document and returns the validated Workflow.
func Parse(data []byte) (*Workflow, error) {
	def, err := ParseDefinition(data)
	if err != nil {
		return nil, err
	}
	return workflowFromDefinition(def), nil
}

// ParseDefinition decodes a workflow YAML document and returns the validated
// semantic workflow definition.
func ParseDefinition(data []byte) (Definition, error) {
	doc, err := syntax.Decode(data, syntax.Source{})
	if err != nil {
		return WorkflowDefinition{}, err
	}
	return DefinitionFromDocument(doc, ParseOptions{})
}

// FromDraft constructs a validated Workflow from a decoded syntax draft.
func FromDraft(draft *syntax.Draft, opts ParseOptions) (*Workflow, error) {
	def, err := DefinitionFromDraft(draft, opts)
	if err != nil {
		return nil, err
	}
	return workflowFromDefinition(def), nil
}

// DefinitionFromDraft constructs a validated semantic workflow definition from
// a decoded syntax draft.
func DefinitionFromDraft(draft *syntax.Draft, opts ParseOptions) (Definition, error) {
	return DefinitionFromDocument((*syntax.Document)(draft), opts)
}

// FromDocument constructs a validated Workflow from a decoded syntax document.
func FromDocument(doc *syntax.Document, opts ParseOptions) (*Workflow, error) {
	def, err := DefinitionFromDocument(doc, opts)
	if err != nil {
		return nil, err
	}
	return workflowFromDefinition(def), nil
}

// DefinitionFromDocument constructs a validated semantic workflow definition
// from a decoded syntax document.
func DefinitionFromDocument(doc *syntax.Document, opts ParseOptions) (Definition, error) {
	p, err := newParser(doc, opts)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	return p.parseDefinition()
}

func newParser(doc *syntax.Document, opts ParseOptions) (*parser, error) {
	if doc == nil {
		return nil, fmt.Errorf("workflow document is nil")
	}
	if opts.Source.Path != "" {
		doc.Source = opts.Source
	}
	id, err := NewWorkflowID(doc.Name)
	if err != nil {
		return nil, err
	}
	return &parser{doc: doc, id: id}, nil
}

func (p *parser) parseDefinition() (Definition, error) {
	decl, err := p.parseDeclarations()
	if err != nil {
		return WorkflowDefinition{}, err
	}

	analyzed, err := analyzeDeclaration(decl)
	if err != nil {
		return WorkflowDefinition{}, err
	}

	return lowerAnalyzedDefinition(analyzed)
}
