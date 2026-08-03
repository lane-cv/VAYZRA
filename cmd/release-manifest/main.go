package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"happylearn.local/app/internal/release"
)

type commandResult struct {
	Status     string `json:"status"`
	Category   string `json:"category"`
	Compatible *bool  `json:"compatible,omitempty"`
}

func main() {
	if run(os.Args[1:], os.Stdout) != 0 {
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) int {
	result, code := execute(args)
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(result); err != nil {
		return 1
	}
	return code
}

func execute(args []string) (commandResult, int) {
	if len(args) == 0 {
		return failed("invalid_arguments"), 1
	}
	switch args[0] {
	case "validate":
		file, ok := parseOneFileFlag("validate", args[1:])
		if !ok {
			return failed("invalid_arguments"), 1
		}
		if _, err := loadManifest(file); err != nil {
			return failed(categoryFor(err)), 1
		}
		return commandResult{Status: "pass", Category: "valid_manifest"}, 0
	case "verify-config":
		set := flag.NewFlagSet("verify-config", flag.ContinueOnError)
		set.SetOutput(io.Discard)
		manifestPath := set.String("file", "", "")
		composePath := set.String("compose", "", "")
		caddyPath := set.String("caddy", "", "")
		if set.Parse(args[1:]) != nil || set.NArg() != 0 || *manifestPath == "" || *composePath == "" || *caddyPath == "" {
			return failed("invalid_arguments"), 1
		}
		manifest, err := loadManifest(*manifestPath)
		if err != nil {
			return failed(categoryFor(err)), 1
		}
		composeHash, err := hashRegularFile(*composePath)
		if err != nil {
			return failed("file_unavailable"), 1
		}
		caddyHash, err := hashRegularFile(*caddyPath)
		if err != nil {
			return failed("file_unavailable"), 1
		}
		if composeHash != manifest.ComposeSHA256 || caddyHash != manifest.CaddySHA256 {
			return failed("configuration_mismatch"), 1
		}
		return commandResult{Status: "pass", Category: "configuration_verified"}, 0
	case "compatible":
		set := flag.NewFlagSet("compatible", flag.ContinueOnError)
		set.SetOutput(io.Discard)
		manifestPath := set.String("file", "", "")
		schema := set.String("schema-version", "", "")
		if set.Parse(args[1:]) != nil || set.NArg() != 0 || *manifestPath == "" || *schema == "" {
			return failed("invalid_arguments"), 1
		}
		version, err := strconv.ParseInt(*schema, 10, 64)
		if err != nil || version < 0 {
			return failed("invalid_arguments"), 1
		}
		manifest, err := loadManifest(*manifestPath)
		if err != nil {
			return failed(categoryFor(err)), 1
		}
		compatible := manifest.CompatibleWithSchema(version)
		if !compatible {
			return commandResult{Status: "fail", Category: "schema_incompatible", Compatible: &compatible}, 1
		}
		return commandResult{Status: "pass", Category: "schema_compatible", Compatible: &compatible}, 0
	default:
		return failed("invalid_arguments"), 1
	}
}

func parseOneFileFlag(name string, args []string) (string, bool) {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	file := set.String("file", "", "")
	if set.Parse(args) != nil || set.NArg() != 0 || *file == "" {
		return "", false
	}
	return *file, true
}

var errInvalidManifest = errors.New("invalid manifest")

func loadManifest(path string) (release.Manifest, error) {
	data, err := readRegularFile(path)
	if err != nil {
		return release.Manifest{}, err
	}
	manifest, err := release.ParseManifest(data)
	if err != nil {
		return release.Manifest{}, errInvalidManifest
	}
	return manifest, nil
}

func readRegularFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("file path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("file unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("file unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("file unavailable")
	}
	return data, nil
}

func hashRegularFile(path string) (string, error) {
	data, err := readRegularFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func failed(category string) commandResult { return commandResult{Status: "fail", Category: category} }

func categoryFor(err error) string {
	if errors.Is(err, errInvalidManifest) {
		return "invalid_manifest"
	}
	return "file_unavailable"
}
