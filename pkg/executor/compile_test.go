package executor

import (
	"strconv"
	"testing"

	"github.com/mcuste/loom/pkg/plan"
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

	prog := mustCompileProgram(t, wf)

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

	prog := mustCompileProgram(t, wf)

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

	prog := mustCompileProgram(t, wf)

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

	prog := mustCompileProgram(t, wf)

	assertUnitKinds(t, prog.units, []string{"loop:0", "task:a", "task:b", "task:c"})
}

func TestCompileProgramBuildsNodeForEveryTask(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "a"},
			{ID: "b", Prompt: "b", DependsOn: []workflow.TaskID{"a"}},
			{ID: "c", Prompt: "c", DependsOn: []workflow.TaskID{"a", "b"}},
		},
	}

	prog := mustCompileProgram(t, wf)

	if len(prog.nodes) != len(wf.Tasks) {
		t.Fatalf("node count = %d, want %d", len(prog.nodes), len(wf.Tasks))
	}
	for i := range wf.Tasks {
		task := &wf.Tasks[i]
		n := prog.nodes[task.ID]
		if n == nil {
			t.Fatalf("nodes[%q] = nil", task.ID)
		}
		if n.id() != task.ID {
			t.Fatalf("nodes[%q].id = %q, want %q", task.ID, n.id(), task.ID)
		}
		if n.task.ID != task.ID {
			t.Fatalf("nodes[%q].task.ID = %q, want %q", task.ID, n.task.ID, task.ID)
		}
		deps := n.deps()
		if len(deps) != len(task.DependsOn) {
			t.Fatalf("nodes[%q].deps len = %d, want %d", task.ID, len(deps), len(task.DependsOn))
		}
		for j := range task.DependsOn {
			if deps[j] != task.DependsOn[j] {
				t.Fatalf("nodes[%q].deps[%d] = %q, want %q", task.ID, j, deps[j], task.DependsOn[j])
			}
		}
		if _, ok := n.op.(invalidOp); ok {
			t.Fatalf("nodes[%q].op = %T, want compiled body op", task.ID, n.op)
		}
	}
}

func TestCompileProgramBuildsLoopMetadata(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "seed"},
			{ID: "outside"},
			{ID: "member_a", DependsOn: []workflow.TaskID{"seed"}},
			{ID: "member_b", DependsOn: []workflow.TaskID{"member_a"}},
			{ID: "member_c", DependsOn: []workflow.TaskID{"member_b", "outside"}},
		},
		Loops: []workflow.LoopGroup{
			{ID: "loop", Members: []workflow.TaskID{"member_a", "member_b", "member_c"}},
		},
	}

	prog := mustCompileProgram(t, wf)

	if len(prog.loops) != 1 {
		t.Fatalf("loop count = %d, want 1", len(prog.loops))
	}
	lp := prog.loops[0]
	if lp == nil {
		t.Fatal("loops[0] = nil")
	}
	if lp.group.ID != wf.Loops[0].ID {
		t.Fatalf("loops[0].group.ID = %q, want %q", lp.group.ID, wf.Loops[0].ID)
	}
	assertTaskIDs(t, lp.members, []workflow.TaskID{"member_a", "member_b", "member_c"})
	assertTaskIDSet(t, lp.memberSet, []workflow.TaskID{"member_a", "member_b", "member_c"})
	assertTaskIDSet(t, lp.entryDeps, []workflow.TaskID{"seed", "outside"})
}

func TestCompileProgramBuildsForEachListSourceEntryDep(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "discover"},
			{ID: "seed"},
			{ID: "handle", DependsOn: []workflow.TaskID{"seed"}},
		},
		Loops: []workflow.LoopGroup{
			{
				ID:         "fan",
				Kind:       workflow.LoopForEach,
				ListSource: "{{discover}}",
				As:         "item",
				Members:    []workflow.TaskID{"handle"},
			},
		},
	}

	prog := mustCompileProgram(t, wf)

	if len(prog.loops) != 1 {
		t.Fatalf("loop count = %d, want 1", len(prog.loops))
	}
	assertTaskIDSet(t, prog.loops[0].entryDeps, []workflow.TaskID{"discover", "seed"})
}

func TestCompileProgramClonesLoopMembers(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "member_a"},
			{ID: "member_b"},
		},
		Loops: []workflow.LoopGroup{
			{ID: "loop", Members: []workflow.TaskID{"member_a", "member_b"}},
		},
	}

	prog := mustCompileProgram(t, wf)
	wf.Loops[0].Members[0] = "mutated"

	assertTaskIDs(t, prog.loops[0].members, []workflow.TaskID{"member_a", "member_b"})
}

func TestCompileProgramBuildsIndependentNodeDeps(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "a"},
			{ID: "b", DependsOn: []workflow.TaskID{"a"}},
		},
	}

	prog := mustCompileProgram(t, wf)
	wf.Tasks[1].DependsOn[0] = "mutated"

	if got := prog.nodes["b"].deps()[0]; got != "a" {
		t.Fatalf("nodes[b].deps[0] = %q, want %q", got, "a")
	}
}

func TestCompileProgramBuildsIndependentNodeAction(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Runtime: "exec-echo",
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "original"},
		},
	}

	prog := mustCompileProgram(t, wf)
	wf.Tasks[0].Prompt = "mutated"

	action, ok := prog.nodes["a"].action().(plan.AskModel)
	if !ok {
		t.Fatalf("nodes[a].action = %T, want plan.AskModel", prog.nodes["a"].action())
	}
	if got := action.Prompt.String(); got != "original" {
		t.Fatalf("nodes[a].action.Prompt = %q, want original", got)
	}
}

func TestCompileProgramUsesParsedDefinitionForTaskPayload(t *testing.T) {
	t.Parallel()

	wf, err := workflow.Parse([]byte(`
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: a
    prompt: original
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wf.ByID("a").Prompt = "mutated"

	prog := mustCompileProgram(t, wf)
	action, ok := prog.nodes["a"].action().(plan.AskModel)
	if !ok {
		t.Fatalf("nodes[a].action = %T, want plan.AskModel", prog.nodes["a"].action())
	}
	if got := action.Prompt.String(); got != "original" {
		t.Fatalf("nodes[a].action.Prompt = %q, want original", got)
	}
	if got := prog.nodes["a"].taskPayload().Prompt; got != "original" {
		t.Fatalf("nodes[a].taskPayload().Prompt = %q, want original", got)
	}
}

func TestCompileProgramCompilesBodyOpByKind(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "prompt", Prompt: "hello"},
			{ID: "shell", Command: "echo hi"},
			{ID: "script", Script: "/tmp/task.sh"},
			{ID: "sub", Workflow: "child"},
			{ID: "invalid"},
		},
	}

	prog := mustCompileProgram(t, wf)

	tests := []struct {
		id   workflow.TaskID
		want any
	}{
		{id: "prompt", want: promptOp{}},
		{id: "shell", want: shellOp{}},
		{id: "script", want: scriptOp{}},
		{id: "sub", want: subWorkflowOp{}},
		{id: "invalid", want: invalidOp{}},
	}

	for _, tc := range tests {
		t.Run(string(tc.id), func(t *testing.T) {
			switch tc.want.(type) {
			case promptOp:
				if _, ok := prog.nodes[tc.id].op.(promptOp); !ok {
					t.Fatalf("nodes[%q].op = %T, want promptOp", tc.id, prog.nodes[tc.id].op)
				}
			case shellOp:
				if _, ok := prog.nodes[tc.id].op.(shellOp); !ok {
					t.Fatalf("nodes[%q].op = %T, want shellOp", tc.id, prog.nodes[tc.id].op)
				}
			case scriptOp:
				if _, ok := prog.nodes[tc.id].op.(scriptOp); !ok {
					t.Fatalf("nodes[%q].op = %T, want scriptOp", tc.id, prog.nodes[tc.id].op)
				}
			case subWorkflowOp:
				if _, ok := prog.nodes[tc.id].op.(subWorkflowOp); !ok {
					t.Fatalf("nodes[%q].op = %T, want subWorkflowOp", tc.id, prog.nodes[tc.id].op)
				}
			case invalidOp:
				if _, ok := prog.nodes[tc.id].op.(invalidOp); !ok {
					t.Fatalf("nodes[%q].op = %T, want invalidOp", tc.id, prog.nodes[tc.id].op)
				}
			default:
				t.Fatalf("unexpected want type %T", tc.want)
			}
		})
	}
}

func mustCompileProgram(t *testing.T, wf *workflow.Workflow) *program {
	t.Helper()
	prog, err := compileProgram(wf)
	if err != nil {
		t.Fatalf("compileProgram: %v", err)
	}
	return prog
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

func assertTaskIDs(t *testing.T, got []workflow.TaskID, want []workflow.TaskID) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("task id count = %d, want %d; got %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("task ids[%d] = %q, want %q; full=%v", i, got[i], want[i], got)
		}
	}
}

func assertTaskIDSet(t *testing.T, got map[workflow.TaskID]bool, want []workflow.TaskID) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("task id set size = %d, want %d; got %v", len(got), len(want), got)
	}
	for _, id := range want {
		if !got[id] {
			t.Fatalf("task id set missing %q; got %v", id, got)
		}
	}
}
