package taskwarrior

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

// newTestClient returns a Client wired with a fake runner. The fake is
// provided by the caller so each test can control stdout/stderr/exit.
func newTestClient(t *testing.T, fake runner) *Client {
	t.Helper()
	return &Client{
		bin: "/usr/bin/task", // non-empty so Available() returns true
		run: fake,
	}
}

// okRunner returns a runner that always succeeds with the given stdout.
func okRunner(stdout []byte) runner {
	return func(_ context.Context, _ ...string) ([]byte, []byte, error) {
		return stdout, nil, nil
	}
}

// errRunner returns a runner that always fails with the given stderr and error.
func errRunner(stderr []byte, err error) runner {
	return func(_ context.Context, _ ...string) ([]byte, []byte, error) {
		return nil, stderr, err
	}
}

// captureRunner records the args it was called with and returns the given stdout.
func captureRunner(stdout []byte, captured *[][]string) runner {
	return func(_ context.Context, args ...string) ([]byte, []byte, error) {
		cp := make([]string, len(args))
		copy(cp, args)
		*captured = append(*captured, cp)
		return stdout, nil, nil
	}
}

// ---- Export / JSON parsing ----

func TestExportParsesFixture(t *testing.T) {
	fixture := mustReadFile(t, "testdata/export.json")
	c := newTestClient(t, okRunner(fixture))

	views, err := c.Export(context.Background())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(views) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(views))
	}

	// First task: minimal fields
	v0 := views[0]
	if v0.ID != "1" {
		t.Errorf("task[0].ID = %q, want %q", v0.ID, "1")
	}
	if v0.UUID != "aaaaaaaa-0000-0000-0000-000000000001" {
		t.Errorf("task[0].UUID = %q", v0.UUID)
	}
	if v0.Description != "Write unit tests" {
		t.Errorf("task[0].Description = %q", v0.Description)
	}
	if v0.Project != "" {
		t.Errorf("task[0].Project = %q, want empty", v0.Project)
	}
	if len(v0.Tags) != 0 {
		t.Errorf("task[0].Tags = %v, want empty", v0.Tags)
	}
	if !v0.Due.IsZero() {
		t.Errorf("task[0].Due = %v, want zero", v0.Due)
	}
	if v0.Urgency != 2.5 {
		t.Errorf("task[0].Urgency = %v, want 2.5", v0.Urgency)
	}

	// Second task: all metadata
	v1 := views[1]
	if v1.ID != "2" {
		t.Errorf("task[1].ID = %q, want %q", v1.ID, "2")
	}
	if v1.Project != "cogitator" {
		t.Errorf("task[1].Project = %q", v1.Project)
	}
	if len(v1.Tags) != 2 || v1.Tags[0] != "review" || v1.Tags[1] != "code" {
		t.Errorf("task[1].Tags = %v", v1.Tags)
	}
	if v1.Priority != "H" {
		t.Errorf("task[1].Priority = %q", v1.Priority)
	}
	wantDue := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if !v1.Due.Equal(wantDue) {
		t.Errorf("task[1].Due = %v, want %v", v1.Due, wantDue)
	}
}

func TestExportEmptyArray(t *testing.T) {
	for _, payload := range [][]byte{[]byte("[]"), []byte(""), []byte("  ")} {
		c := newTestClient(t, okRunner(payload))
		views, err := c.Export(context.Background())
		if err != nil {
			t.Errorf("Export(%q): unexpected error: %v", payload, err)
		}
		if len(views) != 0 {
			t.Errorf("Export(%q): expected empty slice, got %d", payload, len(views))
		}
	}
}

func TestExportMissingFields(t *testing.T) {
	// A task with only id and description — all other fields absent.
	payload := []byte(`[{"id":7,"uuid":"dddddddd-0000-0000-0000-000000000007","description":"minimal","status":"pending"}]`)
	c := newTestClient(t, okRunner(payload))
	views, err := c.Export(context.Background())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("expected 1 task, got %d", len(views))
	}
	v := views[0]
	if v.ID != "7" {
		t.Errorf("ID = %q", v.ID)
	}
	if v.Project != "" || v.Priority != "" || len(v.Tags) != 0 {
		t.Errorf("unexpected non-zero optional fields: project=%q priority=%q tags=%v", v.Project, v.Priority, v.Tags)
	}
	if !v.Due.IsZero() {
		t.Errorf("Due should be zero, got %v", v.Due)
	}
}

func TestExportISOBasicDate(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Time
	}{
		{"20260601T120000Z", time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)},
		{"20000101T000000Z", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"", time.Time{}},
		{"not-a-date", time.Time{}},
	}
	for _, tc := range cases {
		got := parseISOBasic(tc.raw)
		if !got.Equal(tc.want) {
			t.Errorf("parseISOBasic(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// ---- argv construction ----

func TestAddArgvPlainDSL(t *testing.T) {
	var calls [][]string
	c := newTestClient(t, captureRunner(nil, &calls))

	if err := c.Add(context.Background(), "buy milk project:home +errand"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	args := calls[0]
	assertContains(t, args, "rc.confirmation:no")
	assertContains(t, args, "rc.context:")
	assertContains(t, args, "add")
	assertContains(t, args, "buy")
	assertContains(t, args, "milk")
	assertContains(t, args, "project:home")
	assertContains(t, args, "+errand")
}

func TestAddArgvQuotedDescription(t *testing.T) {
	var calls [][]string
	c := newTestClient(t, captureRunner(nil, &calls))

	if err := c.Add(context.Background(), `"two words" project:foo`); err != nil {
		t.Fatalf("Add: %v", err)
	}
	args := calls[0]
	assertContains(t, args, "two words") // quoted span preserved as one token
	assertContains(t, args, "project:foo")
	// "two" and "words" must NOT appear as separate tokens
	for _, a := range args {
		if a == "two" || a == "words" {
			t.Errorf("quoted description was shredded; found bare token %q in %v", a, args)
		}
	}
}

func TestModifyArgv(t *testing.T) {
	var calls [][]string
	c := newTestClient(t, captureRunner(nil, &calls))

	if err := c.Modify(context.Background(), "3", `"new title" priority:H`); err != nil {
		t.Fatalf("Modify: %v", err)
	}
	args := calls[0]
	assertContains(t, args, "rc.confirmation:no")
	assertContains(t, args, "rc.context:")
	assertContains(t, args, "3")
	assertContains(t, args, "modify")
	assertContains(t, args, "new title")
	assertContains(t, args, "priority:H")
}

func TestDoneArgv(t *testing.T) {
	var calls [][]string
	c := newTestClient(t, captureRunner(nil, &calls))

	if err := c.Done(context.Background(), "5"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	args := calls[0]
	assertContains(t, args, "rc.confirmation:no")
	assertContains(t, args, "5")
	assertContains(t, args, "done")
}

func TestDeleteArgv(t *testing.T) {
	var calls [][]string
	c := newTestClient(t, captureRunner(nil, &calls))

	if err := c.Delete(context.Background(), "2"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	args := calls[0]
	assertContains(t, args, "rc.confirmation:no")
	assertContains(t, args, "2")
	assertContains(t, args, "delete")
}

func TestUndoArgv(t *testing.T) {
	var calls [][]string
	c := newTestClient(t, captureRunner(nil, &calls))

	if err := c.Undo(context.Background()); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	args := calls[0]
	assertContains(t, args, "rc.confirmation:no")
	assertContains(t, args, "rc.context:")
	assertContains(t, args, "undo")
}

// ---- Error handling ----

func TestExportNonZeroExitCapturesStderr(t *testing.T) {
	stderrMsg := []byte("task: permission denied")
	c := newTestClient(t, errRunner(stderrMsg, &exec.ExitError{}))

	_, err := c.Export(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "task export") {
		t.Errorf("error missing verb: %v", err)
	}
	if !contains(err.Error(), "permission denied") {
		t.Errorf("error missing stderr: %v", err)
	}
}

func TestAddNonZeroExitCapturesStderr(t *testing.T) {
	stderrMsg := []byte("task: invalid DSL")
	c := newTestClient(t, errRunner(stderrMsg, &exec.ExitError{}))

	err := c.Add(context.Background(), "bad input")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "task add") {
		t.Errorf("error missing verb: %v", err)
	}
	if !contains(err.Error(), "invalid DSL") {
		t.Errorf("error missing stderr: %v", err)
	}
}

func TestExportCtxCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	c := newTestClient(t, func(ctx context.Context, _ ...string) ([]byte, []byte, error) {
		return nil, nil, ctx.Err()
	})

	_, err := c.Export(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestAddCtxTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1)
	defer cancel()
	<-ctx.Done() // ensure expired

	c := newTestClient(t, func(ctx context.Context, _ ...string) ([]byte, []byte, error) {
		return nil, nil, ctx.Err()
	})

	err := c.Add(ctx, "something")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
}

func TestUndoCtxCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := newTestClient(t, func(ctx context.Context, _ ...string) ([]byte, []byte, error) {
		return nil, nil, ctx.Err()
	})

	err := c.Undo(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// ---- tokenizeDSL ----

func TestTokenizeDSL(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{
			input: "buy milk project:home +errand",
			want:  []string{"buy", "milk", "project:home", "+errand"},
		},
		{
			input: `"two words" project:foo`,
			want:  []string{"two words", "project:foo"},
		},
		{
			input: `description:"long name" +tag`,
			want:  []string{"description:long name", "+tag"},
		},
		{
			input: "",
			want:  nil,
		},
		{
			input: "single",
			want:  []string{"single"},
		},
		{
			input: `"only quoted"`,
			want:  []string{"only quoted"},
		},
	}
	for _, tc := range cases {
		got := tokenizeDSL(tc.input)
		if !stringSliceEqual(got, tc.want) {
			t.Errorf("tokenizeDSL(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// ---- Available ----

func TestAvailable(t *testing.T) {
	if (&Client{bin: "/usr/bin/task"}).Available() != true {
		t.Error("non-empty bin should be available")
	}
	if (&Client{bin: ""}).Available() != false {
		t.Error("empty bin should not be available")
	}
	var nilClient *Client
	if nilClient.Available() != false {
		t.Error("nil client should not be available")
	}
}

// ---- helpers ----

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func assertContains(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Errorf("args %v does not contain %q", args, want)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
