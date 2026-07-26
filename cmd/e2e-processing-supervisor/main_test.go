package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"
)

func TestHoldAndReleaseAcknowledgeOnlyAfterTheWorkerStateChanges(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "while :; do sleep 1; done")
	controller, err := startWorker(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = controller.release()
		_ = controller.stop(2 * time.Second)
	})

	handler := controlHandler(controller, "runner-only-token")
	for _, testCase := range []struct {
		path string
		held bool
	}{
		{path: "/hold", held: true},
		{path: "/release", held: false},
	} {
		request := httptest.NewRequest(http.MethodPost, testCase.path, nil)
		request.Header.Set("X-E2E-Control-Token", "runner-only-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%q", testCase.path, response.Code, response.Body.String())
		}
		var acknowledgement struct {
			Held bool `json:"held"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &acknowledgement); err != nil {
			t.Fatalf("%s decode: %v", testCase.path, err)
		}
		if acknowledgement.Held != testCase.held || controller.isHeld() != testCase.held {
			t.Fatalf("%s acknowledgement=%v controller=%v", testCase.path, acknowledgement.Held, controller.isHeld())
		}
	}
}

func TestControlRoutesRejectCallersWithoutTheRunnerToken(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "while :; do sleep 1; done")
	controller, err := startWorker(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.stop(2 * time.Second) })

	response := httptest.NewRecorder()
	controlHandler(controller, "runner-only-token").ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, "/hold", nil),
	)
	if response.Code != http.StatusNotFound || controller.isHeld() {
		t.Fatalf("unauthorized status=%d held=%v", response.Code, controller.isHeld())
	}
}

func TestShutdownReleasesHeldWorkerBeforeStoppingIt(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "while :; do sleep 1; done")
	controller, err := startWorker(command)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.hold(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := shutdownWorker(ctx, controller); err != nil {
		t.Fatal(err)
	}
	if controller.isHeld() {
		t.Fatal("worker remained held after shutdown")
	}
	select {
	case <-controller.done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker process remained after shutdown")
	}
}
