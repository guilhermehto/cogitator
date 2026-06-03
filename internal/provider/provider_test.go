package provider_test

import (
	"testing"
	"time"

	"github.com/guilhermehto/cogitator/internal/harness"
	"github.com/guilhermehto/cogitator/internal/provider"
)

// recordingSink captures ApplyUpdate calls for assertion.
type recordingSink struct {
	updates []provider.SessionUpdate
	removed []struct{ provider harness.Kind; instance, session string }
	cleared []struct{ provider harness.Kind; instance string }
}

func (r *recordingSink) ApplyUpdate(u provider.SessionUpdate) {
	r.updates = append(r.updates, u)
}

func (r *recordingSink) RemoveProviderSession(p harness.Kind, instanceID, sessionID string) {
	r.removed = append(r.removed, struct{ provider harness.Kind; instance, session string }{p, instanceID, sessionID})
}

func (r *recordingSink) ClearProviderInstance(p harness.Kind, instanceID string) {
	r.cleared = append(r.cleared, struct{ provider harness.Kind; instance string }{p, instanceID})
}

// Compile-time check: recordingSink satisfies provider.Sink.
var _ provider.Sink = (*recordingSink)(nil)

func TestSessionUpdateCarriesAllClassifyInputs(t *testing.T) {
	now := time.Now()
	u := provider.SessionUpdate{
		Provider:      harness.Kind("codex"),
		InstanceID:    "codex",
		SessionID:     "ses-1",
		Title:         "My session",
		Slug:          "my-session",
		Directory:     "/home/user/project",
		ParentID:      "parent-1",
		Agent:         "codex-agent",
		StatusType:    "busy",
		HasPermission: true,
		HasQuestion:   false,
		LastError:     time.Time{},
		LastActivity:  now,
		Created:       now.Add(-time.Hour),
		Source:        "live",
	}

	sink := &recordingSink{}
	sink.ApplyUpdate(u)

	if len(sink.updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(sink.updates))
	}
	got := sink.updates[0]
	if got.Provider != harness.Kind("codex") {
		t.Errorf("Provider = %q, want %q", got.Provider, "codex")
	}
	if got.InstanceID != "codex" {
		t.Errorf("InstanceID = %q, want %q", got.InstanceID, "codex")
	}
	if got.SessionID != "ses-1" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "ses-1")
	}
	if got.StatusType != "busy" {
		t.Errorf("StatusType = %q, want %q", got.StatusType, "busy")
	}
	if !got.HasPermission {
		t.Error("HasPermission should be true")
	}
}

func TestSinkInterfaceRemoveAndClear(t *testing.T) {
	sink := &recordingSink{}

	sink.RemoveProviderSession(harness.Kind("codex"), "codex", "ses-1")
	if len(sink.removed) != 1 {
		t.Fatalf("expected 1 removal, got %d", len(sink.removed))
	}
	if sink.removed[0].session != "ses-1" {
		t.Errorf("removed session = %q, want %q", sink.removed[0].session, "ses-1")
	}

	sink.ClearProviderInstance(harness.Kind("opencode"), "127.0.0.1:8080")
	if len(sink.cleared) != 1 {
		t.Fatalf("expected 1 clear, got %d", len(sink.cleared))
	}
	if sink.cleared[0].instance != "127.0.0.1:8080" {
		t.Errorf("cleared instance = %q, want %q", sink.cleared[0].instance, "127.0.0.1:8080")
	}
}
