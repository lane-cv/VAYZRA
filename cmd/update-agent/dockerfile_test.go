package main

import (
	"strings"
	"testing"
)

func TestValidateDockerfileSourcesRequiresImmutableExternalImages(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	frontend := "docker/dockerfile:1.20@" + digest
	external := "golang:1.26.5@" + digest

	for _, test := range []struct {
		name    string
		source  string
		wantErr bool
	}{
		{
			name:    "bare external tag",
			source:  "FROM golang:1.26.5 AS build\n",
			wantErr: true,
		},
		{
			name:    "digest without tag",
			source:  "FROM golang@" + digest + " AS build\n",
			wantErr: true,
		},
		{
			name:   "tag and digest",
			source: "FROM --platform=$BUILDPLATFORM " + external + " AS build\n",
		},
		{
			name: "declared internal stage",
			source: "# syntax=" + frontend + "\n" +
				"FROM " + external + " AS build\n" +
				"FROM build AS test\n",
		},
		{
			name:    "unpinned syntax frontend",
			source:  "# syntax=docker/dockerfile:1.20\nFROM " + external + " AS build\n",
			wantErr: true,
		},
		{
			name:    "BOM before unpinned external source",
			source:  "\ufeffFROM evil:latest AS injected\nFROM " + external + " AS build\n",
			wantErr: true,
		},
		{
			name:    "BOM before unpinned syntax frontend",
			source:  "\ufeff# syntax=docker/dockerfile:1.20\nFROM " + external + " AS build\n",
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateDockerfileSources([]byte(test.source))
			if (err != nil) != test.wantErr {
				t.Fatalf("validate error = %v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}
