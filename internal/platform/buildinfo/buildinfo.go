package buildinfo

import (
	"errors"
	"regexp"
	"strconv"
	"time"
)

var (
	errInvalid = errors.New("invalid build information")
	semver     = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	commit     = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
)

type Info struct {
	Version          string `json:"version"`
	Commit           string `json:"commit"`
	BuiltAt          string `json:"builtAt"`
	MinSchemaVersion int64  `json:"minSchemaVersion"`
	MaxSchemaVersion int64  `json:"maxSchemaVersion"`
}

func Parse(version, revision, builtAt, minSchema, maxSchema string) (Info, error) {
	if !semver.MatchString(version) || !commit.MatchString(revision) {
		return Info{}, errInvalid
	}
	when, err := time.Parse(time.RFC3339, builtAt)
	if err != nil || when.Format(time.RFC3339) != builtAt {
		return Info{}, errInvalid
	}
	minimum, err := strconv.ParseInt(minSchema, 10, 64)
	if err != nil || minimum < 0 {
		return Info{}, errInvalid
	}
	maximum, err := strconv.ParseInt(maxSchema, 10, 64)
	if err != nil || maximum < minimum {
		return Info{}, errInvalid
	}
	return Info{
		Version: version, Commit: revision, BuiltAt: builtAt,
		MinSchemaVersion: minimum, MaxSchemaVersion: maximum,
	}, nil
}

func Development() Info {
	return Info{
		Version: "0.0.0-dev", Commit: "0000000",
		BuiltAt:          "1970-01-01T00:00:00Z",
		MinSchemaVersion: 0, MaxSchemaVersion: 1<<63 - 1,
	}
}
