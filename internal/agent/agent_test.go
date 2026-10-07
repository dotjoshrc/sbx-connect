package agent

import "testing"

func TestLookup(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		a, err := Lookup(name)
		if err != nil || a.Name != name {
			t.Fatalf("%+v %v", a, err)
		}
	}
	if _, err := Lookup("other"); err == nil {
		t.Fatal("accepted unknown agent")
	}
}
