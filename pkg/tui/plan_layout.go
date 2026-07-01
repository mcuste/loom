package tui

import "github.com/mcuste/loom/pkg/workflow"

// planLayout is the shared topology read-model for plan rendering. It carries
// flow order, inline loop anchors, wave groups, shared widths, and the seeded
// top-level count with no formatting decisions.
type planLayout struct {
	flow              []planFlowStep
	waves             []planWave
	flowIDWidth       int
	topTaskIDWidth    int
	topLevelTaskCount int
	loopCount         int
	seededTaskCount   int
	loopBodyIDWidth   map[workflow.LoopID]int
}

type planFlowStep struct {
	taskID    workflow.TaskID
	loopIndex int
}

type planWave struct {
	taskIDs     []workflow.TaskID
	loopIndexes []int
}

func buildPlanLayout(wf *workflow.Workflow, seeded map[workflow.TaskID]bool) planLayout {
	layout := planLayout{
		loopCount:       len(wf.Loops),
		loopBodyIDWidth: make(map[workflow.LoopID]int, len(wf.Loops)),
	}

	entries := wf.AnnotatedPlan()
	loopWave := make(map[workflow.LoopID]int, len(wf.Loops))
	waveCount := 0
	for _, e := range entries {
		if e.Wave+1 > waveCount {
			waveCount = e.Wave + 1
		}
		if e.LoopID == "" {
			if n := len(e.ID); n > layout.topTaskIDWidth {
				layout.topTaskIDWidth = n
			}
			continue
		}
		if w, ok := loopWave[e.LoopID]; !ok || e.Wave < w {
			loopWave[e.LoopID] = e.Wave
		}
	}

	layout.flowIDWidth = layout.topTaskIDWidth
	for i := range wf.Loops {
		lg := &wf.Loops[i]
		if n := len(lg.ID); n > layout.flowIDWidth {
			layout.flowIDWidth = n
		}
		bodyWidth := 0
		for _, id := range lg.Members {
			if n := len(id); n > bodyWidth {
				bodyWidth = n
			}
		}
		layout.loopBodyIDWidth[lg.ID] = bodyWidth
	}

	if waveCount > 0 {
		layout.waves = make([]planWave, waveCount)
		for _, e := range entries {
			if e.LoopID == "" {
				layout.waves[e.Wave].taskIDs = append(layout.waves[e.Wave].taskIDs, e.ID)
			}
		}
		for i := range wf.Loops {
			lg := &wf.Loops[i]
			layout.waves[loopWave[lg.ID]].loopIndexes = append(layout.waves[loopWave[lg.ID]].loopIndexes, i)
		}
	}

	order := make([]workflow.TaskID, 0, len(wf.Tasks))
	for _, id := range wf.Plan() {
		if t := wf.ByID(id); t != nil && t.Loop == "" {
			order = append(order, id)
		}
	}
	layout.topLevelTaskCount = len(order)
	for _, id := range order {
		if seeded[id] {
			layout.seededTaskCount++
		}
	}

	if len(order) == 0 && len(wf.Loops) == 0 {
		return layout
	}

	lastTopIndexByWave := make([]int, waveCount)
	pos := -1
	for wi := range layout.waves {
		if n := len(layout.waves[wi].taskIDs); n > 0 {
			pos += n
		}
		lastTopIndexByWave[wi] = pos
	}

	loopAfter := make(map[int][]int, len(wf.Loops))
	for i := range wf.Loops {
		lg := &wf.Loops[i]
		anchor := -1
		if waveCount > 0 {
			anchor = lastTopIndexByWave[loopWave[lg.ID]]
		}
		loopAfter[anchor] = append(loopAfter[anchor], i)
	}

	layout.flow = make([]planFlowStep, 0, len(order)+len(wf.Loops))
	appendLoops := func(anchor int) {
		for _, i := range loopAfter[anchor] {
			layout.flow = append(layout.flow, planFlowStep{loopIndex: i})
		}
	}
	appendLoops(-1)
	for i, id := range order {
		layout.flow = append(layout.flow, planFlowStep{taskID: id, loopIndex: -1})
		appendLoops(i)
	}
	return layout
}

func (l planLayout) displayedWaveCount() int {
	n := 0
	for _, wave := range l.waves {
		if len(wave.taskIDs) > 0 {
			n++
		}
	}
	return n
}
