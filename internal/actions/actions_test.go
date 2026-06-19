package actions

import (
	"context"
	"testing"

	"winnow/internal/jmap"
)

type fakeJMAP struct {
	ensured     map[string]string // name -> id
	ensureCalls int
	updates     []jmap.EmailUpdate
	updateErr   error
	notUpdated  map[string]string
}

func (f *fakeJMAP) EnsureMailbox(_ context.Context, name string) (string, error) {
	f.ensureCalls++
	if id, ok := f.ensured[name]; ok {
		return id, nil
	}
	return "mb-" + name, nil
}

func (f *fakeJMAP) MailboxByRole(_ context.Context, role string) (jmap.Mailbox, bool, error) {
	return jmap.Mailbox{ID: "mb-" + role, Role: role}, true, nil
}

func (f *fakeJMAP) UpdateEmails(_ context.Context, updates []jmap.EmailUpdate) (map[string]string, error) {
	f.updates = append(f.updates, updates...)
	return f.notUpdated, f.updateErr
}

func TestApplyMoveAndFlag(t *testing.T) {
	f := &fakeJMAP{}
	a := NewApplier(f)
	plans := []Plan{
		{EmailID: "e1", Category: "Promotional", MoveTo: "Promotions", MarkRead: true},
		{EmailID: "e2", Category: "Important", Flag: true}, // kept in inbox, flagged
		{EmailID: "e3", Category: "Needs attention"},       // kept, no change
	}
	res, err := a.Apply(context.Background(), plans, false)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Action != ActionMoved || res[0].Folder != "Promotions" {
		t.Errorf("e1 = %+v", res[0])
	}
	if res[1].Action != ActionFlagged {
		t.Errorf("e2 = %+v", res[1])
	}
	if res[2].Action != ActionKept {
		t.Errorf("e3 = %+v", res[2])
	}

	// Only e1 and e2 produce JMAP updates (e3 is a no-op).
	if len(f.updates) != 2 {
		t.Fatalf("expected 2 updates, got %d: %+v", len(f.updates), f.updates)
	}
	byID := map[string]jmap.EmailUpdate{}
	for _, u := range f.updates {
		byID[u.ID] = u
	}
	if byID["e1"].MailboxIDs["mb-Promotions"] != true {
		t.Errorf("e1 should move to mb-Promotions: %+v", byID["e1"])
	}
	if byID["e1"].SetKeywords["$seen"] != true {
		t.Errorf("e1 should be marked read: %+v", byID["e1"])
	}
	if byID["e2"].SetKeywords["$flagged"] != true || byID["e2"].MailboxIDs != nil {
		t.Errorf("e2 should flag and stay in inbox: %+v", byID["e2"])
	}
}

func TestApplyDryRunMakesNoChanges(t *testing.T) {
	f := &fakeJMAP{}
	a := NewApplier(f)
	res, err := a.Apply(context.Background(), []Plan{
		{EmailID: "e1", MoveTo: "Promotions"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Action != ActionDryRun {
		t.Errorf("dry-run action = %v", res[0].Action)
	}
	if len(f.updates) != 0 {
		t.Errorf("dry-run must not call UpdateEmails, got %+v", f.updates)
	}
}

func TestFolderCache(t *testing.T) {
	f := &fakeJMAP{}
	a := NewApplier(f)
	_, err := a.Apply(context.Background(), []Plan{
		{EmailID: "e1", MoveTo: "Promotions"},
		{EmailID: "e2", MoveTo: "Promotions"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if f.ensureCalls != 1 {
		t.Errorf("EnsureMailbox called %d times, want 1 (cached)", f.ensureCalls)
	}
}

func TestApplyUpdateErrorMarksAffected(t *testing.T) {
	f := &fakeJMAP{updateErr: &jmap.HTTPError{StatusCode: 400, Body: "bad"}}
	a := NewApplier(f)
	res, err := a.Apply(context.Background(), []Plan{{EmailID: "e1", MoveTo: "Promotions"}}, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if res[0].Action != ActionError {
		t.Errorf("expected error action, got %+v", res[0])
	}
}

func TestApplyPartialNotUpdated(t *testing.T) {
	f := &fakeJMAP{notUpdated: map[string]string{"e1": "notFound"}}
	a := NewApplier(f)
	res, err := a.Apply(context.Background(), []Plan{
		{EmailID: "e1", MoveTo: "Promotions"},
		{EmailID: "e2", MoveTo: "Social"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Action != ActionError {
		t.Errorf("e1 should be error (notUpdated): %+v", res[0])
	}
	if res[1].Action != ActionMoved {
		t.Errorf("e2 should be moved: %+v", res[1])
	}
}
