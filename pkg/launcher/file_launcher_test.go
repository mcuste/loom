package launcher

import "testing"

func TestLauncherObserverRequiresFactory(t *testing.T) {
	obs, closeLog, err := (Launcher{}).observer(Provenance{})
	if err == nil {
		t.Fatal("observer error = nil, want missing factory error")
	}
	if obs != nil {
		t.Errorf("observer = %T, want nil", obs)
	}
	if closeLog != nil {
		t.Error("closeLog is non-nil, want nil")
	}
}
