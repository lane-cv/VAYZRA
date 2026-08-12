package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxCandidateDockerfileSize = 1024 * 1024

func validateCandidateBuildInputs(source string) error {
	for _, name := range []string{"Dockerfile", "Dockerfile.worker"} {
		path := filepath.Join(source, name)
		if !pathWithin(source, path) {
			return errors.New("invalid candidate Dockerfile path")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxCandidateDockerfileSize {
			return errors.New("candidate Dockerfile is unavailable")
		}
		file, err := os.Open(path)
		if err != nil {
			return errors.New("candidate Dockerfile is unavailable")
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, maxCandidateDockerfileSize+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(raw) > maxCandidateDockerfileSize {
			return errors.New("candidate Dockerfile is unavailable")
		}
		if err := validateDockerfileSources(raw); err != nil {
			return err
		}
	}
	return nil
}

func validateDockerfileSources(raw []byte) error {
	if len(raw) == 0 || len(raw) > maxCandidateDockerfileSize || !utf8.Valid(raw) {
		return errors.New("candidate Dockerfile is invalid")
	}
	if bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		return errors.New("candidate Dockerfile must not start with a UTF-8 BOM")
	}
	stages := make(map[string]struct{})
	foundFrom := false
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), maxCandidateDockerfileSize)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			directive := strings.TrimSpace(strings.TrimPrefix(line, "#"))
			key, value, ok := strings.Cut(directive, "=")
			if ok && strings.EqualFold(strings.TrimSpace(key), "syntax") && !pinnedDockerImage(strings.TrimSpace(value)) {
				return errors.New("Dockerfile syntax frontend is not pinned by tag and digest")
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		foundFrom = true
		if strings.HasSuffix(line, "\\") {
			return errors.New("continued Dockerfile FROM is not supported")
		}
		index := 1
		for index < len(fields) && strings.HasPrefix(fields[index], "--") {
			if !strings.Contains(fields[index], "=") {
				return errors.New("invalid Dockerfile FROM flag")
			}
			index++
		}
		if index >= len(fields) {
			return errors.New("Dockerfile FROM source is missing")
		}
		image := fields[index]
		index++
		_, internal := stages[strings.ToLower(image)]
		if !internal && !pinnedDockerImage(image) {
			return errors.New("external Dockerfile FROM is not pinned by tag and digest")
		}
		if index == len(fields) {
			continue
		}
		if index+2 != len(fields) || !strings.EqualFold(fields[index], "AS") || !validDockerStage(fields[index+1]) {
			return errors.New("invalid Dockerfile FROM alias")
		}
		stages[strings.ToLower(fields[index+1])] = struct{}{}
	}
	if err := scanner.Err(); err != nil || !foundFrom {
		return errors.New("candidate Dockerfile has no valid FROM")
	}
	return nil
}

func pinnedDockerImage(value string) bool {
	if value == "" || len(value) > 512 || hasControl(value, false) || strings.ContainsAny(value, " $\t\r\n") {
		return false
	}
	at := strings.LastIndex(value, "@")
	if at <= 0 || !validImageID(value[at+1:]) {
		return false
	}
	reference := value[:at]
	lastSlash := strings.LastIndex(reference, "/")
	tag := strings.LastIndex(reference, ":")
	return tag > lastSlash && tag < len(reference)-1
}

func validDockerStage(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '_' && char != '.' && char != '-' {
			return false
		}
	}
	return true
}
