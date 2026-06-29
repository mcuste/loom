package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/mcuste/loom/pkg/schedule"
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
	if len(recs) == 0 {
		_, err := fmt.Fprintln(w, "no schedules")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tWORKFLOW\tTRIGGER\tNEXT FIRE\tENABLED\tOVERLAP")
	for _, r := range recs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.WorkflowID, triggerSummary(r.Trigger), formatFireTime(r.NextFire), pick(r.Enabled, "yes", "no"), r.EffectiveOverlap())
	}
	return tw.Flush()
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
	_, err = fmt.Fprintf(w, "%s %s\n", pick(enabled, "enabled", "disabled"), id)
	return err
}
