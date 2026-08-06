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

	raid, _ := h.storage.GetRAIDStatus(r.Context())
	usage, _ := h.storage.GetDiskUsage(r.Context())

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
