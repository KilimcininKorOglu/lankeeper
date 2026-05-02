package handlers

import (
	"log"
	"net/http"

	"github.com/KilimcininKorOglu/home-router/internal/i18n"
	"github.com/KilimcininKorOglu/home-router/internal/services"
	"github.com/KilimcininKorOglu/home-router/internal/tmpl"
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
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}
