package restart

import (
	"testing"
)

func TestRequestInjected(t *testing.T) {
	dir := t.TempDir()
	called := false
	c := New(dir, func() { called = true })
	res := c.Request()
	if res["action"] != "restart_scheduled" || !called {
		t.Fatalf("%v called=%v", res, called)
	}
	res2 := c.Request()
	if res2["action"] != "restart_already_scheduled" {
		t.Fatalf("%v", res2)
	}
}
