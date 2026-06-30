package main

import (
	"fmt"
	"io"

	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/tui"
)

func doScheduleList(w io.Writer, workflowFilter string) error {
	home, err := loomHome()
	if err != nil {
		return err
	}
	recs, err := schedule.List(home, workflowFilter)
	if err != nil {
		return err
	}
	return tui.SchedulesTable(w, recs)
}

func doScheduleRemove(w io.Writer, id string) error {
	home, err := loomHome()
	if err != nil {
		return err
	}
	if err := schedule.Remove(home, id); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "removed %s\n", id)
	return err
}

func doScheduleToggle(w io.Writer, id string, enabled bool) error {
	home, err := loomHome()
	if err != nil {
		return err
	}
	rec, err := schedule.Get(home, id)
	if err != nil {
		return err
	}
	rec.Enabled = enabled
	if err := schedule.Update(home, rec); err != nil {
		return err
	}
	label := "disabled"
	if enabled {
		label = "enabled"
	}
	_, err = fmt.Fprintf(w, "%s %s\n", label, id)
	return err
}
