// Package syntax decodes workflow YAML into draft structs without assigning
// domain meaning.
package syntax

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Source identifies where a draft came from.
type Source struct {
	Path string
}

// Document is the raw decoded workflow shape.
type Document struct {
	Name         string         `yaml:"name"`
	Description  string         `yaml:"description"`
	Runtime      string         `yaml:"runtime"`
	Model        string         `yaml:"model"`
	Effort       string         `yaml:"effort"`
	SystemPrompt string         `yaml:"system_prompt"`
	Params       Value          `yaml:"params"`
	Tasks        []DraftTask    `yaml:"tasks"`
	Budget       Value          `yaml:"budget"`
	Cache        *bool          `yaml:"cache"`
	WorkingDir   string         `yaml:"working_dir"`
	Output       string         `yaml:"output"`
	Schedule     *DraftSchedule `yaml:"schedule"`
	Source       Source         `yaml:"-"`
}

// Draft is a backwards-compatible alias for Document.
type Draft = Document

// DraftWorkflow is a backwards-compatible alias for Document.
type DraftWorkflow = Document

// DraftSchedule mirrors an inline schedule block.
type DraftSchedule struct {
	Cron string `yaml:"cron"`
	TZ   string `yaml:"tz"`
}

// DraftTask mirrors the task YAML schema.
type DraftTask struct {
	ID               string   `yaml:"id"`
	Description      string   `yaml:"description"`
	Runtime          string   `yaml:"runtime"`
	Model            string   `yaml:"model"`
	Effort           string   `yaml:"effort"`
	Prompt           string   `yaml:"prompt"`
	Command          string   `yaml:"command"`
	SystemPrompt     string   `yaml:"system_prompt"`
	SystemPromptFile string   `yaml:"system_prompt_file"`
	Workflow         string   `yaml:"workflow"`
	Script           string   `yaml:"script"`
	Args             []string `yaml:"args"`
	OkExit           []int    `yaml:"ok_exit"`
	PromptFile       string   `yaml:"prompt_file"`
	DependsOn        []string `yaml:"depends_on"`
	WritesState      string   `yaml:"writes_state"`
	When             string   `yaml:"when"`
	Retry            Value    `yaml:"retry"`
	ForEach          Value    `yaml:"for_each"`
	ForEachParallel  Value    `yaml:"for_each_parallel"`
	Budget           Value    `yaml:"budget"`
	Schema           Value    `yaml:"schema"`
	Cache            *bool    `yaml:"cache"`
	Loop             Value    `yaml:"loop"`
	With             Value    `yaml:"with"`
}

// DraftParam mirrors the scalar fields of one params entry.
type DraftParam struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
}

// Decode parses YAML into a Draft, rejecting keys outside the known schema.
func Decode(data []byte, source Source) (*Draft, error) {
	var draft Document
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&draft); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	draft.Source = source
	return &draft, nil
}
