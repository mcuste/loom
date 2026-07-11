package workflow

// buildTaskNode lowers one analyzed task declaration into the semantic node
// model. All validation has already happened in analyzeTaskDeclaration, so this
// step is a pure declaration plus analysis -> Definition projection.
func buildTaskNode(rt taskDecl, analysis taskAnalysis, loopVar string) TaskNode {
	meta := analysis.meta
	return TaskNode{
		ID:          rt.id,
		Description: rt.description,
		DependsOn:   nodeIDs(meta.deps),
		Action:      taskActionFromDecl(rt, analysis.withArgs, loopVar),
		Condition:   meta.cond,
		When:        rt.when,
		Runtime: RuntimeSelector{
			Runtime: rt.runtime,
			Model:   rt.model,
			Effort:  rt.effort,
		},
		Policies: TaskPolicies{
			Retry:  meta.retry,
			Budget: meta.budget,
			Cache:  rt.cache,
			Schema: meta.schema,
			OkExit: append([]int(nil), rt.okExit...),
		},
		WritesState:  rt.writesState,
		Loop:         rt.loop,
		SystemPrompt: ParseTemplate(rt.systemPrompt),
	}
}
