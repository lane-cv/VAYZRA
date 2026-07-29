package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"happylearn.local/app/internal/operations"

	"golang.org/x/sys/unix"
)

const (
	maxInputBytes = 64 * 1024
	maxRows       = 16
	maxSecretSize = 8 * 1024
	maxExactValue = int64(1<<53 - 1)
	maxClockSkew  = 90 * time.Second
)

var (
	noncePattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	percentPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]*)(?:\.[0-9]+)?%$`)
	bytesPattern   = regexp.MustCompile(`^(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:B|kB|KB|KiB|MB|MiB|GB|GiB|TB|TiB)$`)
	errInvalid     = errors.New("invalid input")
)

type samplerInput struct {
	SchemaVersion int                   `json:"schemaVersion"`
	ObservedAt    time.Time             `json:"observedAt"`
	Compose       []composeServiceInput `json:"compose"`
	Stats         []statsInput          `json:"stats"`
	Filesystems   []filesystemInput     `json:"filesystems"`
}

type composeServiceInput struct {
	Service  string `json:"service"`
	State    string `json:"state"`
	Health   string `json:"health"`
	Restarts int64  `json:"restarts"`
}

type statsInput struct {
	Service     string `json:"service"`
	CPUPercent  string `json:"cpuPercent"`
	MemoryUsage string `json:"memoryUsage"`
}

type filesystemInput struct {
	Filesystem  string `json:"filesystem"`
	UsedPercent string `json:"usedPercent"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, time.Now))
}

func run(
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	clock func() time.Time,
) int {
	if stdin == nil || stdout == nil || stderr == nil || clock == nil {
		return fail(stderr)
	}
	if len(args) == 0 || (len(args) == 1 && args[0] == "payload") {
		if err := emitPayload(stdin, stdout, clock().UTC()); err != nil {
			return fail(stderr)
		}
		return 0
	}
	if len(args) > 0 && args[0] == "sign" {
		if err := emitSignature(args[1:], stdin, stdout, clock().UTC()); err != nil {
			return fail(stderr)
		}
		return 0
	}
	return fail(stderr)
}

func fail(stderr io.Writer) int {
	if stderr != nil {
		_, _ = io.WriteString(stderr, "host sampler: invalid input\n")
	}
	return 1
}

func emitPayload(stdin io.Reader, stdout io.Writer, now time.Time) error {
	body, err := readBounded(stdin, maxInputBytes)
	if err != nil {
		return errInvalid
	}
	var input samplerInput
	if err := decodeStrict(body, &input); err != nil {
		return errInvalid
	}
	payload, err := buildPayload(input, now)
	if err != nil {
		return errInvalid
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return errInvalid
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxInputBytes {
		return errInvalid
	}
	if _, err := stdout.Write(encoded); err != nil {
		return errInvalid
	}
	return nil
}

func buildPayload(input samplerInput, now time.Time) (operations.HostPayload, error) {
	if input.SchemaVersion != 1 ||
		!validObservedAt(input.ObservedAt, now) ||
		len(input.Compose)+len(input.Filesystems) == 0 ||
		len(input.Compose)+len(input.Stats)+len(input.Filesystems) > maxRows {
		return operations.HostPayload{}, errInvalid
	}

	composeByService := make(map[string]composeServiceInput, len(input.Compose))
	for _, row := range input.Compose {
		if !allowedService(row.Service) ||
			!allowedState(row.State) ||
			!allowedHealth(row.Health) ||
			row.Restarts < 0 ||
			row.Restarts > maxExactValue {
			return operations.HostPayload{}, errInvalid
		}
		if _, duplicate := composeByService[row.Service]; duplicate {
			return operations.HostPayload{}, errInvalid
		}
		composeByService[row.Service] = row
	}

	statsByService := make(map[string]statsInput, len(input.Stats))
	for _, row := range input.Stats {
		compose, exists := composeByService[row.Service]
		if !exists || compose.State != "running" {
			return operations.HostPayload{}, errInvalid
		}
		if _, duplicate := statsByService[row.Service]; duplicate {
			return operations.HostPayload{}, errInvalid
		}
		statsByService[row.Service] = row
	}

	payload := operations.HostPayload{
		SchemaVersion: 1,
		ObservedAt:    input.ObservedAt.UTC(),
		Services:      make([]operations.HostServiceSample, 0, len(composeByService)),
		Filesystems:   make([]operations.FilesystemSample, 0, len(input.Filesystems)),
	}
	for _, service := range serviceOrder {
		compose, exists := composeByService[service]
		if !exists {
			continue
		}
		sample := operations.HostServiceSample{
			Service:  service,
			Up:       compose.State == "running" && (compose.Health == "" || compose.Health == "healthy"),
			Restarts: compose.Restarts,
		}
		stats, hasStats := statsByService[service]
		if compose.State == "running" && !hasStats {
			return operations.HostPayload{}, errInvalid
		}
		if hasStats {
			cpu, err := parsePercent(stats.CPUPercent)
			if err != nil {
				return operations.HostPayload{}, errInvalid
			}
			used, limit, err := parseMemoryUsage(stats.MemoryUsage)
			if err != nil || used > limit {
				return operations.HostPayload{}, errInvalid
			}
			sample.CPUPercent = cpu
			sample.MemoryBytes = used
			sample.MemoryLimitBytes = limit
		}
		payload.Services = append(payload.Services, sample)
	}
	if len(statsByService) > len(payload.Services) {
		return operations.HostPayload{}, errInvalid
	}

	seenFilesystems := make(map[string]struct{}, len(input.Filesystems))
	filesystemByName := make(map[string]operations.FilesystemSample, len(input.Filesystems))
	for _, row := range input.Filesystems {
		if row.Filesystem != "root" && row.Filesystem != "backup" {
			return operations.HostPayload{}, errInvalid
		}
		if _, duplicate := seenFilesystems[row.Filesystem]; duplicate {
			return operations.HostPayload{}, errInvalid
		}
		used, err := parsePercent(row.UsedPercent)
		if err != nil {
			return operations.HostPayload{}, errInvalid
		}
		seenFilesystems[row.Filesystem] = struct{}{}
		filesystemByName[row.Filesystem] = operations.FilesystemSample{
			Filesystem: row.Filesystem, UsedPercent: used,
		}
	}
	for _, filesystem := range []string{"root", "backup"} {
		if sample, exists := filesystemByName[filesystem]; exists {
			payload.Filesystems = append(payload.Filesystems, sample)
		}
	}
	return payload, nil
}

var serviceOrder = []string{"caddy", "app", "worker", "postgres", "redis", "minio"}

func allowedService(service string) bool {
	index := sort.SearchStrings(serviceOrderSorted, service)
	return index < len(serviceOrderSorted) && serviceOrderSorted[index] == service
}

var serviceOrderSorted = []string{"app", "caddy", "minio", "postgres", "redis", "worker"}

func allowedState(state string) bool {
	switch state {
	case "created", "running", "restarting", "exited", "paused", "dead":
		return true
	default:
		return false
	}
}

func allowedHealth(health string) bool {
	switch health {
	case "", "healthy", "unhealthy", "starting":
		return true
	default:
		return false
	}
}

func validObservedAt(observedAt time.Time, now time.Time) bool {
	_, offset := observedAt.Zone()
	return !observedAt.IsZero() &&
		offset == 0 &&
		observedAt.Nanosecond() == 0 &&
		!observedAt.Before(now.Add(-maxClockSkew)) &&
		!observedAt.After(now.Add(maxClockSkew))
}

func parsePercent(value string) (float64, error) {
	if !percentPattern.MatchString(value) {
		return 0, errInvalid
	}
	parsed, err := strconv.ParseFloat(strings.TrimSuffix(value, "%"), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) ||
		parsed < 0 || parsed > 100 {
		return 0, errInvalid
	}
	return parsed, nil
}

func parseMemoryUsage(value string) (int64, int64, error) {
	parts := strings.Split(value, " / ")
	if len(parts) != 2 {
		return 0, 0, errInvalid
	}
	used, err := parseBytes(parts[0])
	if err != nil {
		return 0, 0, errInvalid
	}
	limit, err := parseBytes(parts[1])
	if err != nil {
		return 0, 0, errInvalid
	}
	return used, limit, nil
}

func parseBytes(value string) (int64, error) {
	if !bytesPattern.MatchString(value) {
		return 0, errInvalid
	}
	unitStart := 0
	for unitStart < len(value) &&
		(value[unitStart] == '.' ||
			value[unitStart] >= '0' && value[unitStart] <= '9') {
		unitStart++
	}
	number := new(big.Rat)
	if _, ok := number.SetString(value[:unitStart]); !ok {
		return 0, errInvalid
	}
	multiplier, ok := byteMultipliers[value[unitStart:]]
	if !ok {
		return 0, errInvalid
	}
	number.Mul(number, new(big.Rat).SetInt64(multiplier))
	if !number.IsInt() || !number.Num().IsInt64() {
		return 0, errInvalid
	}
	result := number.Num().Int64()
	if result < 0 || result > maxExactValue {
		return 0, errInvalid
	}
	return result, nil
}

var byteMultipliers = map[string]int64{
	"B":   1,
	"kB":  1000,
	"KB":  1000,
	"KiB": 1 << 10,
	"MB":  1000 * 1000,
	"MiB": 1 << 20,
	"GB":  1000 * 1000 * 1000,
	"GiB": 1 << 30,
	"TB":  1000 * 1000 * 1000 * 1000,
	"TiB": 1 << 40,
}

func emitSignature(
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	now time.Time,
) error {
	flags := flag.NewFlagSet("sign", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var secretPath, timestampText, nonce string
	flags.StringVar(&secretPath, "secret-file", "", "")
	flags.StringVar(&timestampText, "timestamp", "", "")
	flags.StringVar(&nonce, "nonce", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errInvalid
	}
	timestampSeconds, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil || strconv.FormatInt(timestampSeconds, 10) != timestampText {
		return errInvalid
	}
	timestamp := time.Unix(timestampSeconds, 0).UTC()
	if timestamp.Before(now.Add(-maxClockSkew)) ||
		timestamp.After(now.Add(maxClockSkew)) ||
		!noncePattern.MatchString(nonce) {
		return errInvalid
	}
	body, err := readBounded(stdin, maxInputBytes)
	if err != nil || validateCanonicalPayload(body, timestamp) != nil {
		return errInvalid
	}
	secret, err := readOwnerOnlySecret(secretPath)
	if err != nil {
		return errInvalid
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(timestampText))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(nonce))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write(body)
	if _, err := fmt.Fprintf(stdout, "sha256=%s\n", hex.EncodeToString(mac.Sum(nil))); err != nil {
		return errInvalid
	}
	return nil
}

func validateCanonicalPayload(body []byte, timestamp time.Time) error {
	var payload operations.HostPayload
	if err := decodeStrict(body, &payload); err != nil {
		return errInvalid
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return errInvalid
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(body, canonical) ||
		payload.SchemaVersion != 1 ||
		!validObservedAt(payload.ObservedAt, timestamp) ||
		len(payload.Services)+len(payload.Filesystems) == 0 ||
		len(payload.Services)+len(payload.Filesystems) > maxRows {
		return errInvalid
	}
	services := make(map[string]struct{}, len(payload.Services))
	previousServiceRank := -1
	for _, row := range payload.Services {
		rank := serviceRank(row.Service)
		if !allowedService(row.Service) ||
			rank <= previousServiceRank ||
			row.CPUPercent < 0 || row.CPUPercent > 100 ||
			math.IsNaN(row.CPUPercent) || math.IsInf(row.CPUPercent, 0) ||
			row.MemoryBytes < 0 ||
			row.MemoryLimitBytes < 0 ||
			row.MemoryBytes > row.MemoryLimitBytes ||
			row.MemoryLimitBytes > maxExactValue ||
			row.Restarts < 0 || row.Restarts > maxExactValue {
			return errInvalid
		}
		if _, duplicate := services[row.Service]; duplicate {
			return errInvalid
		}
		services[row.Service] = struct{}{}
		previousServiceRank = rank
	}
	filesystems := make(map[string]struct{}, len(payload.Filesystems))
	previousFilesystemRank := -1
	for _, row := range payload.Filesystems {
		rank := filesystemRank(row.Filesystem)
		if (row.Filesystem != "root" && row.Filesystem != "backup") ||
			rank <= previousFilesystemRank ||
			row.UsedPercent < 0 || row.UsedPercent > 100 ||
			math.IsNaN(row.UsedPercent) || math.IsInf(row.UsedPercent, 0) {
			return errInvalid
		}
		if _, duplicate := filesystems[row.Filesystem]; duplicate {
			return errInvalid
		}
		filesystems[row.Filesystem] = struct{}{}
		previousFilesystemRank = rank
	}
	return nil
}

func serviceRank(service string) int {
	for index, allowed := range serviceOrder {
		if service == allowed {
			return index
		}
	}
	return -1
}

func filesystemRank(filesystem string) int {
	if filesystem == "root" {
		return 0
	}
	if filesystem == "backup" {
		return 1
	}
	return -1
}

func decodeStrict(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errInvalid
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errInvalid
	}
	return nil
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil || int64(len(body)) > maximum {
		return nil, errInvalid
	}
	return body, nil
}

func readOwnerOnlySecret(path string) ([]byte, error) {
	if path == "" {
		return nil, errInvalid
	}
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, errInvalid
	}
	file := os.NewFile(uintptr(fd), "host-hmac")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errInvalid
	}
	defer file.Close()

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil ||
		stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		(stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) ||
		stat.Mode&0o077 != 0 ||
		stat.Size > maxSecretSize {
		return nil, errInvalid
	}
	body, err := readBounded(file, maxSecretSize)
	if err != nil {
		return nil, errInvalid
	}
	if bytes.HasSuffix(body, []byte("\r\n")) {
		body = body[:len(body)-2]
	} else if bytes.HasSuffix(body, []byte("\n")) {
		body = body[:len(body)-1]
	}
	if len(body) == 0 {
		return nil, errInvalid
	}
	return body, nil
}
