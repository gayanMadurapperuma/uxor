package api

import (
	"fmt"

	"example.com/shop/store"
)

// Handler serves user pages.
type Handler struct {
	store.Repository
	repo store.Repository
}

func NewHandler() *Handler {
	return &Handler{repo: store.NewMemRepo()}
}

func (h *Handler) Show(id int) string {
	u, err := h.repo.GetUserByID(id)
	if err != nil {
		return fmt.Sprint(err)
	}
	return h.format(u)
}

func (h *Handler) format(u *store.User) string {
	return u.Name
}

// Render looks a user up through any repository.
func Render(repo store.Repository, id int) string {
	u, _ := repo.GetUserByID(id)
	return u.Name
}
