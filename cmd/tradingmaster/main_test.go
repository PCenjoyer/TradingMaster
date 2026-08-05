package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := healthcheck(server.URL); err != nil {
		t.Fatalf("проверка здоровья завершилась ошибкой: %v", err)
	}
}

func TestHealthcheckRejectsUnhealthyService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	if err := healthcheck(server.URL); err == nil {
		t.Fatal("ожидалась ошибка нездорового сервиса")
	}
}
