package state

import (
	"testing"
	"time"
)

func TestClassifyMarksOnlyBusyAndGeneratingAsActive(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name       string
		statusType string
		want       Attention
	}{
		{name: "busy", statusType: "busy", want: AttnActive},
		{name: "generating", statusType: "generating", want: AttnActive},
		{name: "idle", statusType: "idle", want: AttnInactive},
		{name: "empty", statusType: "", want: AttnInactive},
		{name: "retry", statusType: "retry", want: AttnInactive},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.statusType, false, time.Time{}, now)
			if got != tc.want {
				t.Fatalf("Classify(%q) = %q, want %q", tc.statusType, got, tc.want)
			}
		})
	}
}

func TestClassifyKeepsPermissionAndErrorPrecedence(t *testing.T) {
	now := time.Now()

	if got := Classify("busy", true, time.Time{}, now); got != AttnPermissionPending {
		t.Fatalf("permission precedence: got %q, want %q", got, AttnPermissionPending)
	}

	errorTime := now.Add(-time.Second)
	if got := Classify("busy", false, errorTime, errorTime); got != AttnErrored {
		t.Fatalf("error precedence: got %q, want %q", got, AttnErrored)
	}

	if got := Classify("busy", false, errorTime, now); got != AttnActive {
		t.Fatalf("newer activity should clear error: got %q, want %q", got, AttnActive)
	}
}
