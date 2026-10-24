package store

import (
	"errors"
	str "strings"
)

// ErrNotFound is returned when a user is missing.
var ErrNotFound = errors.New("not found")

const (
	MaxUsers   = 100
	defaultTTL = 30
)

// User is a customer account.
type User struct {
	ID   int
	Name string
}

// Repository loads users.
type Repository interface {
	GetUserByID(id int) (*User, error)
}

type userID = int

type memRepo struct {
	users map[int]*User
}

// NewMemRepo builds an in-memory repository.
func NewMemRepo() *memRepo {
	return &memRepo{users: make(map[int]*User)}
}

func (r *memRepo) GetUserByID(id int) (*User, error) {
	u, ok := r.users[id]
	if !ok {
		return nil, ErrNotFound
	}
	return u, nil
}

func normalize(name string) string {
	return str.ToLower(str.TrimSpace(name))
}

// ValidNames keeps the names that normalize to something.
func ValidNames(names []string) []string {
	keep := func(n string) bool {
		return normalize(n) != ""
	}
	var out []string
	for _, n := range names {
		if keep(n) {
			out = append(out, n)
		}
	}
	return out
}
