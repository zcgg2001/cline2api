package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestPortConflictLeavesExistingServerRunning(t *testing.T) {
	isolatedAdmin(t)
	existing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "existing-service") }))
	defer existing.Close()
	host, portString, err := net.SplitHostPort(existing.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portString)
	if err := startProxy(host, port); err == nil {
		t.Fatal("port conflict was ignored")
	}
	resp, err := existing.Client().Get(existing.URL)
	if err != nil {
		t.Fatalf("existing service interrupted: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "existing-service" {
		t.Fatal("existing service replaced")
	}
}
