package registry

import "slices"

// workflowExts is the workflow file extensions in precedence order: .yaml is
// probed before .yml. Centralised here so both Resolve and walkRegistry use
// the same list and cannot drift.
var workflowExts = []string{".yaml", ".yml"}

// IsWorkflowExt reports whether ext (as returned by filepath.Ext, leading dot
// included) names a workflow file.
func IsWorkflowExt(ext string) bool {
	return slices.Contains(workflowExts, ext)
}

// precedence returns the resolution priority for a registry entry: a lower
// number wins (probed/shadowed first). A flat form (collapsed=false) has
// higher priority than an eponymous-dir form (collapsed=true). Both
// candidateStems (resolve probe order) and walkRegistry (shadow logic) derive
// flat-over-dir ordering from this single function so the rule lives once.
func precedence(collapsed bool) int {
	if !collapsed {
		return 0 // flat: wins
	}
	return 1 // dir form: lower priority
}
