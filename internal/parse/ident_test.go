package parse

import (
	"reflect"
	"testing"
)

func TestSplit(t *testing.T) {
	cases := map[string][]string{
		"HTTPServerError":  {"http", "server", "error"},
		"get_user_by_id":   {"get", "user", "by", "id"},
		"getUserById":      {"get", "user", "by", "id"},
		"parseURL2":        {"parse", "url", "2"},
		"x":                {"x"},
		"__init__":         {"init"},
		"store.NewMemRepo": {"store", "new", "mem", "repo"},
		"src/a-b/c.ts":     {"src", "a", "b", "c", "ts"},
		"":                 nil,
	}
	for in, want := range cases {
		if got := Split(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Split(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSplitText(t *testing.T) {
	if got := SplitText("getUserById"); got != "0getUserById get user by id" {
		t.Errorf("got %q", got)
	}
	if got := SplitText("user"); got != "user" {
		t.Errorf("got %q", got)
	}
}
