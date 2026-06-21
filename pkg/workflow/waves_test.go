package workflow_test

import (
	"reflect"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestWaves_GroupsByLongestPathLevel pins the wave grouping for the canonical
// DAG shapes. A task's wave is the longest dependency path from any root, so a
// diamond's join sits one level below its deepest branch, not its shallowest.
// Within a wave, declaration order is preserved (matching Plan's tie-break).
func TestWaves_GroupsByLongestPathLevel(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want [][]workflow.TaskID
	}{
		{
			name: "chain",
			src: `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: x
  - id: b
    depends_on: [a]
    prompt: x
  - id: c
    depends_on: [b]
    prompt: x
`,
			want: [][]workflow.TaskID{{"a"}, {"b"}, {"c"}},
		},
		{
			name: "fan-out",
			src: `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: x
  - id: b
    depends_on: [a]
    prompt: x
  - id: c
    depends_on: [a]
    prompt: x
`,
			want: [][]workflow.TaskID{{"a"}, {"b", "c"}},
		},
		{
			name: "diamond",
			src: `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: x
  - id: b
    depends_on: [a]
    prompt: x
  - id: c
    depends_on: [a]
    prompt: x
  - id: d
    depends_on: [b, c]
    prompt: x
`,
			want: [][]workflow.TaskID{{"a"}, {"b", "c"}, {"d"}},
		},
		{
			name: "disconnected",
			src: `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: x
  - id: c
    prompt: x
  - id: b
    depends_on: [a]
    prompt: x
  - id: d
    depends_on: [c]
    prompt: x
`,
			want: [][]workflow.TaskID{{"a", "c"}, {"b", "d"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wf, err := workflow.Parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := wf.Waves()
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Waves() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWaves_DoesNotMutateWorkflow pins that Waves is pure: it reads the graph
// without disturbing any state a later call depends on. Rather than assert the
// language-level invariant that a slice Waves never writes to stays put, it
// checks the observable one: a second Waves call (and the Plan it relies on)
// returns the same result as the first.
func TestWaves_DoesNotMutateWorkflow(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: x
  - id: b
    depends_on: [a]
    prompt: x
  - id: c
    depends_on: [a]
    prompt: x
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	planBefore := wf.Plan()
	wavesBefore := wf.Waves()

	// A second pass must reproduce both the topological order and the wave
	// grouping; divergence would mean Waves left the graph (or the byID index it
	// walks) in an altered state.
	if got := wf.Plan(); !reflect.DeepEqual(got, planBefore) {
		t.Fatalf("Plan changed after Waves: got %v, want %v", got, planBefore)
	}
	if got := wf.Waves(); !reflect.DeepEqual(got, wavesBefore) {
		t.Fatalf("Waves not idempotent: got %v, want %v", got, wavesBefore)
	}
}
