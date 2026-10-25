package parse

import (
	"reflect"
	"strings"
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
	for in, want := range map[string]string{
		"user":       "user",
		"formatUser": "0formatUser format user",
		// Word-aligned parts of 2+ words, so "signIn" reaches handleSignIn
		// but no prefix crosses a word (D38).
		"handleSignIn": "0handleSignIn 0handlesign 0signin handle sign in",
		"getUserById":  "0getUserById 0getuser 0getuserby 0userby 0userbyid 0byid get user by id",
	} {
		if got := SplitText(in); got != want {
			t.Errorf("SplitText(%q) = %q, want %q", in, got, want)
		}
	}
	// Past maxPartWords words only parts of 2-3 words are added.
	long := SplitText("getUserProfileImageUrlFromCache") // 7 words
	if !strings.Contains(long, " 0getuserprofile ") || strings.Contains(long, " 0getuserprofileimage ") {
		t.Errorf("long identifier parts: %q", long)
	}
}
