package workflow

import (
	"fmt"
	"os"
	"path/filepath"
)

// ReadAndParse is the canonical "load a workflow file" primitive: it reads path,
// inlines its `prompt_file:`/`system_prompt_file:` references and anchors its
// relative `script:` paths relative to the YAML's own directory, then parses the
// resulting self-contained bytes. It returns both the parsed workflow and the
// inlined manifest (the bytes a caller persists), and deliberately stops before
// the registry-dependent routing check so it stays a pure function of the file
// content. [ParseFile] layers ValidateRouting on top; callers that need the
// manifest (e.g. before a chdir) use ReadAndParse directly.
func ReadAndParse(path string) (*Workflow, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	// Inline any `prompt_file:` references relative to the YAML's own directory
	// before Parse, so Parse only ever sees inline `prompt:` bodies.
	manifest, err := InlinePromptFiles(data, filepath.Dir(path))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	// Anchor relative `script:` paths so the self-contained manifest resolves the
	// same script regardless of the cwd at run, resume, or daemon-reload time.
	manifest, err = AnchorScriptPaths(manifest, filepath.Dir(path))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	// Anchor a relative `working_dir:` the same way, so the persisted manifest
	// names an absolute cwd and a stored-manifest resume (which Parses the bytes
	// with no baseDir) agrees on where tasks run.
	manifest, err = AnchorWorkingDir(manifest, filepath.Dir(path))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	wf, err := Parse(manifest)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return wf, manifest, nil
}

// ParseFile reads path and parses it as a workflow YAML.
func ParseFile(path string) (*Workflow, error) {
	wf, _, err := ReadAndParse(path)
	if err != nil {
		return nil, err
	}
	// ParseFile loads a workflow for execution, so it runs the registry-dependent
	// routing check that ReadAndParse deliberately omits. Sub-workflow children
	// are not linked yet here; the caller re-runs ValidateRouting after linking to
	// cover them via the w.Subs recursion.
	if err := wf.ValidateRouting(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return wf, nil
}
