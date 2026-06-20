package workflow

import (
	"slices"
	"testing"
)

// TestScanPlaceholders_ReturnsTaskAndParamRefsInSourceOrder pins the contract
// for the shared single-pass placeholder scanner: it must classify each
// `{{...}}` token into a task-id ref or a param ref, preserve source order
// within each slice, and ignore tokens that fail the strict placeholder shape
// (malformed braces). Behavioral test over the returned values only.
func TestScanPlaceholders_ReturnsTaskAndParamRefsInSourceOrder(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		wantTasks  []string
		wantParams []string
	}{
		{
			name:       "mixed task and param refs",
			text:       "use {{a}} and {{params.x}} then {{b}} with {{params.y}}",
			wantTasks:  []string{"a", "b"},
			wantParams: []string{"x", "y"},
		},
		{
			name:       "interleaved refs preserve source order",
			text:       "{{params.x}} {{a}} {{params.y}} {{b}}",
			wantTasks:  []string{"a", "b"},
			wantParams: []string{"x", "y"},
		},
		{
			name:       "param prefix disambiguates from bare task id",
			text:       "{{params.foo}}",
			wantTasks:  nil,
			wantParams: []string{"foo"},
		},
		{
			name:       "bare id is a task ref not a param ref",
			text:       "{{foo}}",
			wantTasks:  []string{"foo"},
			wantParams: nil,
		},
		{
			name:       "ignores malformed braces",
			text:       "{{params.x.y}} {{ params.z }} {{good}} {{params.ok}}",
			wantTasks:  []string{"good"},
			wantParams: []string{"ok"},
		},
		{
			name:       "repeated placeholders are kept once per occurrence",
			text:       "{{a}} {{a}} {{params.x}} {{params.x}}",
			wantTasks:  []string{"a", "a"},
			wantParams: []string{"x", "x"},
		},
		{
			name:       "no placeholders yields empty slices",
			text:       "plain text with no tokens",
			wantTasks:  nil,
			wantParams: nil,
		},
		{
			name:       "empty input yields empty slices",
			text:       "",
			wantTasks:  nil,
			wantParams: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotTasks, gotParams := scanPlaceholders(tc.text)
			if !equalStringSlices(gotTasks, tc.wantTasks) {
				t.Errorf("scanPlaceholders(%q) taskRefs = %v, want %v", tc.text, gotTasks, tc.wantTasks)
			}
			if !equalStringSlices(gotParams, tc.wantParams) {
				t.Errorf("scanPlaceholders(%q) paramRefs = %v, want %v", tc.text, gotParams, tc.wantParams)
			}
		})
	}
}

// equalStringSlices compares two string slices, treating nil and empty as
// equal so the test does not pin nil-vs-zero-length as a behavioral contract.
func equalStringSlices(a, b []string) bool {
	return slices.Equal(a, b)
}
