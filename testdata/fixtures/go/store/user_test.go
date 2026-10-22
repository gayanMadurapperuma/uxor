package store

import "testing"

func TestGetUserByID(t *testing.T) {
	r := NewMemRepo()
	if _, err := r.GetUserByID(1); err == nil {
		t.Fatal("expected error")
	}
}
