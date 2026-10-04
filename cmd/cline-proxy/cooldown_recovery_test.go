package main

import (
	"testing"
	"time"
)

func TestCooldownRecoveryStopWaitsForWorkerExit(t *testing.T) {
	stop := startCooldownRecovery()
	stopped := make(chan struct{})
	go func() {
		stop()
		stop() // Cancellation and waiting remain safe when called again.
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("recovery stop waited for the next 30-second tick")
	}
}
