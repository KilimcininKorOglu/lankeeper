package handlers

import (
	"log"
	"net/http"

	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/tmpl"
)

type StorageHandler struct {
	renderer *tmpl.Renderer
	storage  *services.StorageService
}

func NewStorageHandler(renderer *tmpl.Renderer, storage *services.StorageService) *StorageHandler {
	return &StorageHandler{renderer: renderer, storage: storage}
}

func (h *StorageHandler) HandlePage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.LangFromContext(r.Context())

	// Both cards are optional: a router without an array has no RAID
	// status, so a failure hides the card and is logged, not answered.
	raid, err := h.storage.GetRAIDStatus(r.Context())
	if err != nil {
		log.Printf("storage: raid status: %v", err)
	}
	usage, err := h.storage.GetDiskUsage(r.Context())
	if err != nil {
		log.Printf("storage: disk usage: %v", err)
	}

	data := &tmpl.PageData{
		Lang: lang,
		Page: "storage",
		Data: map[string]any{
			"RAID":  raid,
			"Usage": usage,
		},
	}

	if err := h.renderer.Render(w, "storage", "base", data); err != nil {
		log.Printf("render storage: %v", err)
		clientError(w, r, http.StatusInternalServerError, "error.internal")
	}
}
