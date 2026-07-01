package executor

import (
	"strconv"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

func TestCompileProgramWithoutLoopsBuildsTaskUnitsFromPlan(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "c", DependsOn: []workflow.TaskID{"b"}},
			{ID: "a"},
			{ID: "b", DependsOn: []workflow.TaskID{"a"}},
			{ID: "d"},
		},
	}

	prog := compileProgram(wf)

	if len(prog.memberOf) != 0 {
		t.Fatalf("memberOf size = %d, want 0", len(prog.memberOf))
	}
	assertUnitKinds(t, prog.units, []string{"task:a", "task:b", "task:c", "task:d"})
}

func TestCompileProgramWithLoopExcludesMembersFromTopLevelTasks(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "seed"},
			{ID: "member_a", DependsOn: []workflow.TaskID{"seed"}},
			{ID: "member_b", DependsOn: []workflow.TaskID{"member_a"}},
			{ID: "after", DependsOn: []workflow.TaskID{"member_b"}},
		},
		Loops: []workflow.LoopGroup{
			{ID: "loop", Members: []workflow.TaskID{"member_a", "member_b"}},
		},
	}

	prog := compileProgram(wf)

	if got := prog.memberOf["member_a"]; got != 0 {
		t.Fatalf("memberOf[member_a] = %d, want 0", got)
	}
	if got := prog.memberOf["member_b"]; got != 0 {
		t.Fatalf("memberOf[member_b] = %d, want 0", got)
	}
	assertUnitKinds(t, prog.units, []string{"loop:0", "task:seed", "task:after"})
}

func TestCompileProgramPreservesLoopDeclarationOrder(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "outer"},
			{ID: "first_member", DependsOn: []workflow.TaskID{"outer"}},
			{ID: "second_member", DependsOn: []workflow.TaskID{"outer"}},
		},
		Loops: []workflow.LoopGroup{
			{ID: "first", Members: []workflow.TaskID{"first_member"}},
			{ID: "second", Members: []workflow.TaskID{"second_member"}},
		},
	}

	prog := compileProgram(wf)

	assertUnitKinds(t, prog.units, []string{"loop:0", "loop:1", "task:outer"})
}

func TestCompileProgramPreservesTopLevelPlanOrder(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "c", DependsOn: []workflow.TaskID{"b"}},
			{ID: "a"},
			{ID: "b", DependsOn: []workflow.TaskID{"a"}},
			{ID: "d"},
		},
		Loops: []workflow.LoopGroup{
			{ID: "loop", Members: []workflow.TaskID{"d"}},
		},
	}

	prog := compileProgram(wf)

	assertUnitKinds(t, prog.units, []string{"loop:0", "task:a", "task:b", "task:c"})
}

func assertUnitKinds(t *testing.T, units []unit, want []string) {
	t.Helper()

	got := make([]string, 0, len(units))
	for _, u := range units {
		switch v := u.(type) {
		case taskUnit:
			got = append(got, "task:"+string(v.id))
		case loopUnit:
			got = append(got, "loop:"+strconv.Itoa(v.index))
		default:
			t.Fatalf("unexpected unit type %T", u)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("unit count = %d, want %d; got %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("units[%d] = %q, want %q; full=%v", i, got[i], want[i], got)
		}
	}
}
