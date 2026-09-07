package sessions

import (
	"testing"
)

func TestCreateAppendList(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Create("Hello")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Title != "Hello" {
		t.Fatal(sess.Title)
	}
	out, err := s.AppendMessage(sess.ID, "user", "hi there", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 {
		t.Fatal(len(out.Messages))
	}
	got, err := s.Get(sess.ID)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	list := s.ListSessions()
	if len(list) != 1 || list[0].MessageCount != 1 {
		t.Fatalf("%+v", list)
	}
}

func TestDemoSeed(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.EnsureDemoSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(created) < 2 {
		t.Fatalf("expected demo sessions, got %d", len(created))
	}
	again, _ := s.EnsureDemoSessions()
	if again != nil {
		t.Fatal("should not re-seed")
	}
}
