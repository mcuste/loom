package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Sentinel errors for the inline/system-prompt-file logic.
var (
	// ErrSystemPromptAndFileSet reports a workflow that sets both the inline
	// `system_prompt:` and `system_prompt_file:`. The two are mutually exclusive:
	// system_prompt_file is just the file-backed spelling of system_prompt.
	ErrSystemPromptAndFileSet = errors.New("workflow sets both system_prompt and system_prompt_file")

	// ErrTaskSystemPromptAndFileSet is the task-level counterpart: a task that
	// sets both `system_prompt:` and `system_prompt_file:`.
	ErrTaskSystemPromptAndFileSet = errors.New("task sets both system_prompt and system_prompt_file")
)

// InlinePromptFiles rewrites file-reference keys into their inline equivalents
// by reading the referenced files relative to baseDir:
//
//   - every task's `prompt_file:` becomes an inline `prompt:`,
//   - a top-level `system_prompt_file:` becomes an inline `system_prompt:`, and
//   - every task's `system_prompt_file:` becomes an inline `system_prompt:`.
//
// For task prompts it walks the YAML node tree and, for each mapping node that
// carries a `prompt_file` key, enforces the 5-way body-form mutual exclusivity
// (a task sets at most one of prompt, prompt_file, command, loop, for_each),
// rejects absolute paths, reads the file at filepath.Join(baseDir, path), and
// replaces the `prompt_file` key+value with a `prompt` whose literal-block value
// is the file content. The walk covers nested loop bodies, so a `prompt_file`
// inside a `loop:` or `for_each:` body is inlined the same way.
//
// A `system_prompt_file:` is inlined both on the document's root mapping (the
// workflow-level default, handled by inlineSystemPromptFile) and on any task
// mapping (a per-task override, handled by the node walk). Setting both the
// inline and file spellings on the same scope is rejected with
// ErrSystemPromptAndFileSet (workflow) or ErrTaskSystemPromptAndFileSet (task).
//
// The rewritten bytes are self-contained: Parse never sees either *_file key, so
// KnownFields(true) strictness is preserved. InlinePromptFiles short-circuits
// with no YAML round-trip when the raw bytes contain no `prompt_file` token
// (which also covers `system_prompt_file`, since it contains that substring).
func InlinePromptFiles(data []byte, baseDir string) ([]byte, error) {
	// Fast path: the overwhelming majority of workflows carry no `prompt_file`
	// key, so skip the unmarshal + node walk + marshal round-trip entirely when
	// the token is absent from the raw bytes. `system_prompt_file` contains the
	// `prompt_file` substring, so this guard catches it too.
	if !bytes.Contains(data, []byte("prompt_file")) {
		return data, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if err := inlineSystemPromptFile(&doc, baseDir); err != nil {
		return nil, err
	}
	if err := inlinePromptFileNodes(&doc, baseDir); err != nil {
		return nil, err
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	return out, nil
}

// AnchorScriptPaths rewrites every task's relative `script:` path to an absolute
// path anchored at baseDir, so a script task resolves to the same file no matter
// what directory loom runs in: a fresh run, a `check`, a resume that restored a
// different cwd, or a daemon reload. Without it a relative `script:` would be
// resolved against the process cwd at exec time, which is both surprising and
// not portable.
//
// It is the script analogue of InlinePromptFiles: the rewrite is baked into the
// returned bytes, so the persisted manifest stays self-contained and every later
// Parse of those bytes (including a stored-manifest resume that has no baseDir)
// agrees on the path.
//
// Only plain relative paths are anchored. An absolute path is left as-is, and a
// path carrying a `{{...}}` template is left untouched so a dynamically built
// path is resolved against the run's cwd after substitution rather than having
// baseDir prepended to a half-formed value. Like InlinePromptFiles it
// short-circuits with no YAML round-trip when the raw bytes contain no `script`
// token.
func AnchorScriptPaths(data []byte, baseDir string) ([]byte, error) {
	if !bytes.Contains(data, []byte("script")) {
		return data, nil
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	anchorScriptNodes(&doc, absBase)
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	return out, nil
}

// anchorScriptNodes recurses the node tree, anchoring the value of every
// `script:` key it finds against baseDir (already made absolute by the caller).
// Walking the whole tree (not just top-level tasks) means a `script:` inside a
// nested loop or for_each body is anchored the same way.
func anchorScriptNodes(n *yaml.Node, baseDir string) {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind != yaml.ScalarNode || k.Value != "script" || v.Kind != yaml.ScalarNode {
				continue
			}
			if s := v.Value; s != "" && !filepath.IsAbs(s) && !strings.Contains(s, "{{") {
				v.Value = filepath.Join(baseDir, s)
			}
		}
		// Keys are scalars that never hold a nested body, so recurse into values only.
		for i := 1; i < len(n.Content); i += 2 {
			anchorScriptNodes(n.Content[i], baseDir)
		}
		return
	}
	for _, c := range n.Content {
		anchorScriptNodes(c, baseDir)
	}
}

// AnchorWorkingDir rewrites a relative top-level `working_dir:` to an absolute
// path anchored at baseDir, so the cwd tasks run in is fixed at load time rather
// than resolved against the process cwd at exec time. It is the working_dir
// analogue of AnchorScriptPaths: the rewrite is baked into the returned bytes so
// the persisted manifest stays self-contained and a stored-manifest resume
// (which has no baseDir) agrees on the directory.
//
// Only the document's root mapping is considered, since working_dir is a
// workflow-level key; a `working_dir` nested in a task mapping is left for
// Parse's known-fields check to reject. An absolute value is left as-is, and a
// value carrying a `{{...}}` template is left untouched so a dynamically built
// path resolves after substitution rather than having baseDir prepended to a
// half-formed value. It short-circuits with no YAML round-trip when the raw
// bytes contain no `working_dir` token.
func AnchorWorkingDir(data []byte, baseDir string) ([]byte, error) {
	if !bytes.Contains(data, []byte("working_dir")) {
		return data, nil
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return data, nil
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return data, nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Value != "working_dir" || v.Kind != yaml.ScalarNode {
			continue
		}
		if s := v.Value; s != "" && !filepath.IsAbs(s) && !strings.Contains(s, "{{") {
			v.Value = filepath.Join(absBase, s)
		}
		break
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	return out, nil
}

// inlineSystemPromptFile rewrites a top-level `system_prompt_file:` key into an
// inline `system_prompt:` by reading the referenced file relative to baseDir.
//
// Only the document's root mapping is considered: system_prompt is a
// workflow-level field, so a `system_prompt_file` nested in a task mapping is
// left untouched for Parse's known-fields check to reject in context. A workflow
// that sets both `system_prompt` and `system_prompt_file` is rejected with
// ErrSystemPromptAndFileSet. The rules otherwise mirror task prompt_file:
// absolute paths are rejected, and a read failure surfaces as a
// SystemPromptFileError wrapping the OS error.
func inlineSystemPromptFile(doc *yaml.Node, baseDir string) error {
	root := doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil
	}
	var keyNode, value *yaml.Node
	hasInline := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		switch k.Value {
		case "system_prompt_file":
			keyNode, value = k, v
		case "system_prompt":
			hasInline = true
		}
	}
	if keyNode == nil {
		return nil
	}
	if hasInline {
		return ErrSystemPromptAndFileSet
	}
	if filepath.IsAbs(value.Value) {
		return &AbsoluteSystemPromptFilePathError{Path: value.Value}
	}
	content, err := os.ReadFile(filepath.Join(baseDir, value.Value))
	if err != nil {
		return &SystemPromptFileError{Path: value.Value, Err: err}
	}
	keyNode.Value = "system_prompt"
	value.Kind = yaml.ScalarNode
	value.Tag = "!!str"
	value.Style = yaml.LiteralStyle
	value.Value = string(content)
	return nil
}

// inlinePromptFileNodes recurses the node tree, inlining `prompt_file` in every
// mapping it visits. Walking the whole tree (not just top-level tasks) means a
// `prompt_file` inside a nested loop body is inlined the same way.
func inlinePromptFileNodes(n *yaml.Node, baseDir string) error {
	if n.Kind == yaml.MappingNode {
		if err := inlinePromptFileMapping(n, baseDir); err != nil {
			return err
		}
		// A mapping's Content alternates key/value; keys are scalars that can
		// never hold a nested body, so recurse into the value nodes only.
		for i := 1; i < len(n.Content); i += 2 {
			if err := inlinePromptFileNodes(n.Content[i], baseDir); err != nil {
				return err
			}
		}
		return nil
	}
	for _, c := range n.Content {
		if err := inlinePromptFileNodes(c, baseDir); err != nil {
			return err
		}
	}
	return nil
}

// inlinePromptFileMapping inlines a single task mapping's `prompt_file` and
// `system_prompt_file` keys in place. The two are independent: a task may carry
// either, both, or neither (a `prompt_file` body alongside a `system_prompt_file`
// override is legal), so each is handled on its own rather than short-circuiting
// when the other is absent. A mapping without an `id` is left untouched.
type taskFileRefs struct {
	taskID          TaskID
	pfKey, pfVal    *yaml.Node
	spfKey, spfVal  *yaml.Node
	bodyFields      []string
	hasID           bool
	hasSystemPrompt bool
}

func scanTaskFileRefs(m *yaml.Node) taskFileRefs {
	var refs taskFileRefs
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		switch k.Value {
		case "id":
			refs.taskID = TaskID(v.Value)
			refs.hasID = true
		case "prompt_file":
			refs.pfKey, refs.pfVal = k, v
			refs.bodyFields = append(refs.bodyFields, k.Value)
		case "loop", "for_each":
			// Loop wrapper forms are not in bodyForms (they are split off before
			// Parse runs the body-form check), but they are still mutually exclusive
			// with prompt_file, so track them here.
			refs.bodyFields = append(refs.bodyFields, k.Value)
		case "system_prompt_file":
			refs.spfKey, refs.spfVal = k, v
		case "system_prompt":
			refs.hasSystemPrompt = true
		default:
			if isBodyFormKey(k.Value) {
				refs.bodyFields = append(refs.bodyFields, k.Value)
			}
		}
	}
	return refs
}

func inlineTaskPromptFile(refs taskFileRefs, baseDir string) error {
	if refs.pfKey == nil {
		return nil
	}
	// prompt_file is one of the mutually exclusive body forms: any sibling body
	// key is a conflict, reported with every offending field in declaration order.
	if len(refs.bodyFields) > 1 {
		return &TaskBodyConflictError{Task: refs.taskID, Fields: refs.bodyFields}
	}
	if filepath.IsAbs(refs.pfVal.Value) {
		return &AbsolutePromptFilePathError{Task: refs.taskID, Path: refs.pfVal.Value}
	}
	content, err := os.ReadFile(filepath.Join(baseDir, refs.pfVal.Value))
	if err != nil {
		return &PromptFileError{Task: refs.taskID, Path: refs.pfVal.Value, Err: err}
	}
	refs.pfKey.Value = "prompt"
	refs.pfVal.Kind = yaml.ScalarNode
	refs.pfVal.Tag = "!!str"
	refs.pfVal.Style = yaml.LiteralStyle
	refs.pfVal.Value = string(content)
	return nil
}

func inlineTaskSystemPromptFile(refs taskFileRefs, baseDir string) error {
	if refs.spfKey == nil {
		return nil
	}
	// system_prompt_file is not a body form; it is the file-backed spelling of
	// system_prompt and mutually exclusive with the inline key, mirroring the
	// workflow-level rule.
	if refs.hasSystemPrompt {
		return fmt.Errorf("task %q: %w", refs.taskID, ErrTaskSystemPromptAndFileSet)
	}
	// Wrap with the task id so a multi-task workflow points at the offending
	// task, matching the prompt_file errors above; the wrapped sentinel types
	// stay reachable via errors.As / errors.Is.
	if filepath.IsAbs(refs.spfVal.Value) {
		return fmt.Errorf("task %q: %w", refs.taskID, &AbsoluteSystemPromptFilePathError{Path: refs.spfVal.Value})
	}
	content, err := os.ReadFile(filepath.Join(baseDir, refs.spfVal.Value))
	if err != nil {
		return fmt.Errorf("task %q: %w", refs.taskID, &SystemPromptFileError{Path: refs.spfVal.Value, Err: err})
	}
	refs.spfKey.Value = "system_prompt"
	refs.spfVal.Kind = yaml.ScalarNode
	refs.spfVal.Tag = "!!str"
	refs.spfVal.Style = yaml.LiteralStyle
	refs.spfVal.Value = string(content)
	return nil
}

func inlinePromptFileMapping(m *yaml.Node, baseDir string) error {
	refs := scanTaskFileRefs(m)
	// Only task mappings legitimately carry these file refs, and every task has an
	// `id`. A stray `prompt_file` / `system_prompt_file` in any other mapping
	// (schema body, loop block) is left untouched for Parse's known-fields check
	// to reject in context, rather than inlined or reported against an empty task
	// id. The workflow-root `system_prompt_file` has no id and is handled by
	// inlineSystemPromptFile, which runs before this walk.
	if !refs.hasID {
		return nil
	}
	if err := inlineTaskPromptFile(refs, baseDir); err != nil {
		return err
	}
	return inlineTaskSystemPromptFile(refs, baseDir)
}

// AbsolutePromptFilePathError reports a `prompt_file:` whose value is an
// absolute path. Only paths relative to the workflow file's own directory are
// permitted; this keeps workflows self-contained and shareable.
type AbsolutePromptFilePathError struct {
	Task TaskID
	Path string
}

func (e *AbsolutePromptFilePathError) Error() string {
	return fmt.Sprintf("task %q: prompt_file %q must be a relative path", e.Task, e.Path)
}

// PromptFileError reports a `prompt_file:` that could not be read (file missing,
// permission denied, or any other I/O failure). Err wraps the underlying OS
// error so errors.Is(err, os.ErrNotExist) works for callers that need to
// distinguish "file not found" from other failures.
type PromptFileError struct {
	Task TaskID
	Path string
	Err  error
}

func (e *PromptFileError) Error() string {
	return fmt.Sprintf("task %q: read prompt_file %q: %v", e.Task, e.Path, e.Err)
}

func (e *PromptFileError) Unwrap() error { return e.Err }

// AbsoluteSystemPromptFilePathError reports a `system_prompt_file:` (workflow- or
// task-level) whose value is an absolute path. Only paths relative to the
// workflow file's own directory are permitted, matching the prompt_file rule.
type AbsoluteSystemPromptFilePathError struct {
	Path string
}

func (e *AbsoluteSystemPromptFilePathError) Error() string {
	return fmt.Sprintf("system_prompt_file %q must be a relative path", e.Path)
}

// SystemPromptFileError reports a `system_prompt_file:` (workflow- or task-level)
// that could not be read (file missing, permission denied, or any other I/O
// failure). Err wraps the underlying OS error so errors.Is(err, os.ErrNotExist)
// works.
type SystemPromptFileError struct {
	Path string
	Err  error
}

func (e *SystemPromptFileError) Error() string {
	return fmt.Sprintf("read system_prompt_file %q: %v", e.Path, e.Err)
}

func (e *SystemPromptFileError) Unwrap() error { return e.Err }
