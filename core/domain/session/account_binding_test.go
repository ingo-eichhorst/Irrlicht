package session

import "testing"

func TestAccountBinding_Advance(t *testing.T) {
	a := HomeAccount{Ref: "A", Since: 50}
	cases := []struct {
		name  string
		b     AccountBinding
		home  HomeAccount
		start int64
		now   int64
		want  AccountBinding
	}{
		{"binds when home predates session", AccountBinding{}, a, 100, 120, AccountBinding{Decided: true, Ref: "A", ConfirmedAt: 120}},
		{"binds when Since equals start", AccountBinding{}, a, 50, 60, AccountBinding{Decided: true, Ref: "A", ConfirmedAt: 60}},
		{"unbound when home observed after start", AccountBinding{}, a, 49, 60, AccountBinding{Decided: true}},
		{"unbound when logged out", AccountBinding{}, HomeAccount{Since: 10}, 100, 120, AccountBinding{Decided: true}},
		{"decided unbound never binds later", AccountBinding{Decided: true}, a, 100, 120, AccountBinding{Decided: true}},
		{"same account refreshes ConfirmedAt", AccountBinding{Decided: true, Ref: "A", ConfirmedAt: 120}, a, 0, 200, AccountBinding{Decided: true, Ref: "A", ConfirmedAt: 200}},
		{"switch clears as of the last confirmation", AccountBinding{Decided: true, Ref: "A", ConfirmedAt: 250}, HomeAccount{Ref: "B", Since: 300}, 0, 310,
			AccountBinding{Decided: true, Ref: "A", ConfirmedAt: 250, ClearedAt: 250}},
		{"logout clears", AccountBinding{Decided: true, Ref: "A", ConfirmedAt: 250}, HomeAccount{Since: 300}, 0, 310,
			AccountBinding{Decided: true, Ref: "A", ConfirmedAt: 250, ClearedAt: 250}},
		{"switch with no confirmation clears everything", AccountBinding{Decided: true, Ref: "A"}, HomeAccount{Ref: "B", Since: 300}, 0, 310,
			AccountBinding{Decided: true, Ref: "A", ClearedAt: 1}},
		{"switching back never re-binds", AccountBinding{Decided: true, Ref: "A", ConfirmedAt: 250, ClearedAt: 250}, HomeAccount{Ref: "A", Since: 400}, 0, 410,
			AccountBinding{Decided: true, Ref: "A", ConfirmedAt: 250, ClearedAt: 250}},
		{"second switch keeps first clear", AccountBinding{Decided: true, Ref: "A", ConfirmedAt: 250, ClearedAt: 250}, HomeAccount{Ref: "C", Since: 500}, 0, 510,
			AccountBinding{Decided: true, Ref: "A", ConfirmedAt: 250, ClearedAt: 250}},
	}
	for _, c := range cases {
		if got := c.b.Advance(c.home, c.start, c.now); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestAccountBinding_RefAt(t *testing.T) {
	b := AccountBinding{Decided: true, Ref: "A", ConfirmedAt: 300, ClearedAt: 300}
	for at, want := range map[int64]string{0: "A", 299: "A", 300: "A", 301: "", 1000: ""} {
		if got := b.RefAt(at); got != want {
			t.Errorf("RefAt(%d) = %q, want %q", at, got, want)
		}
	}
	if got := (AccountBinding{Decided: true, Ref: "A"}).RefAt(1 << 40); got != "A" {
		t.Errorf("uncleared RefAt = %q, want A", got)
	}
	if got := (AccountBinding{Decided: true}).RefAt(5); got != "" {
		t.Errorf("unbound RefAt = %q, want empty", got)
	}
}
