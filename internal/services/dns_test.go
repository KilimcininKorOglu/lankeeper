package services_test

import (
	"context"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

func TestNewDNSService(t *testing.T) {
	cfg := &config.Config{}
	svc := services.NewDNSService(cfg)
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}

func TestQueryLogEmpty(t *testing.T) {
	cfg := &config.Config{}
	svc := services.NewDNSService(cfg)

	queries := svc.GetRecentQueries(10, 0)
	if len(queries) != 0 {
		t.Errorf("expected 0 queries, got %d", len(queries))
	}
}

func TestClearQueryLog(t *testing.T) {
	cfg := &config.Config{}
	svc := services.NewDNSService(cfg)

	err := svc.ClearQueryLog(context.TODO())
	if err != nil {
		t.Fatalf("clear: %v", err)
	}

	queries := svc.GetRecentQueries(10, 0)
	if len(queries) != 0 {
		t.Error("should be empty after clear")
	}
}
