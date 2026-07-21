package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"happylearn.local/app/internal/platform/config"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/objectstore"
	"happylearn.local/app/internal/processing"
)

const (
	workerHealthAddress = "127.0.0.1:8081"
	workerShutdownLimit = 20 * time.Second
	readinessLimit      = 5 * time.Second
	commandOutputLimit  = 64 * 1024
)

var requiredCommands = map[string][]string{
	"clamscan": {"--version"},
	"soffice":  {"--version"},
	"pdfinfo":  {"-v"},
	"ffprobe":  {"-version"},
}

func main() {
	if err := run(); err != nil {
		log.Print("worker_error")
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return errors.New("worker configuration")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return errors.New("worker database")
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		return errors.New("worker migration")
	}
	stores, err := objectstore.NewMinIO(ctx, objectstore.MinIOConfig{Endpoint: cfg.MinIOEndpoint, AccessKey: cfg.MinIOAccessKey, SecretKey: cfg.MinIOSecretKey, UseTLS: cfg.MinIOUseTLS, OriginalsBucket: cfg.MinIOOriginalsBucket, PreviewsBucket: cfg.MinIOPreviewsBucket})
	if err != nil {
		return errors.New("worker object storage")
	}
	workDir := os.Getenv("HAPPYLEARN_WORK_DIR")
	if workDir == "" {
		workDir = "/work"
	}
	ready := workerReadiness(pool.Ping, stores.Ready, workDir, runtime.GOOS == "linux", requiredCommands)
	startupCtx, startupCancel := context.WithTimeout(ctx, readinessLimit)
	defer startupCancel()
	if err := ready(startupCtx); err != nil {
		return errors.New("worker dependencies")
	}

	owner, err := workerOwner()
	if err != nil {
		return errors.New("worker identity")
	}
	processingStore := processing.NewPostgresStore(pool)
	worker, err := buildWorker(processingStore, owner, func() (processing.Processor, error) {
		return newProductionProcessor(processingStore, stores.Originals, stores.Previews, workDir)
	})
	if err != nil {
		return errors.New("worker processing pipeline")
	}
	health := &http.Server{Addr: workerHealthAddress, Handler: healthHandler(ready), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	workerDone := make(chan error, 1)
	healthDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(ctx) }()
	go func() { healthDone <- health.ListenAndServe() }()

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerExited := false
	var workerErr error
	select {
	case <-signalCtx.Done():
		cancel()
	case workerErr = <-workerDone:
		workerExited = true
		cancel()
	case err := <-healthDone:
		cancel()
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("worker health")
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), workerShutdownLimit)
	defer shutdownCancel()
	_ = health.Shutdown(shutdownCtx)
	if workerExited {
		if workerErr != nil {
			return errors.New("worker lifecycle")
		}
		return nil
	}
	select {
	case err := <-workerDone:
		if err != nil {
			return errors.New("worker shutdown")
		}
		return nil
	case <-shutdownCtx.Done():
		return errors.New("worker shutdown timeout")
	}
}

type processorFactory func() (processing.Processor, error)

func newProductionProcessor(sources processing.SourceStore, originals, previews processing.BlobStore, workDir string) (processing.Processor, error) {
	if sources == nil || originals == nil || previews == nil || !filepath.IsAbs(workDir) {
		return nil, errors.New("processing pipeline unavailable")
	}
	return &processing.Pipeline{Sources: sources, Originals: originals, Previews: previews, Runner: processing.ExecRunner{}, WorkRoot: workDir, ClamDefinitionsDir: "/var/lib/clamav"}, nil
}

func buildWorker(store processing.Store, owner string, factory processorFactory) (*processing.Worker, error) {
	if store == nil || owner == "" || factory == nil {
		return nil, errors.New("invalid worker wiring")
	}
	processor, err := factory()
	if err != nil || processor == nil {
		return nil, errors.New("processing pipeline unavailable")
	}
	return &processing.Worker{Store: store, Processor: processor, Owner: owner}, nil
}

func workerOwner() (string, error) {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "", errors.New("hostname")
	}
	return host + ":" + uuid.NewString(), nil
}

func workerReadiness(databaseReady, objectReady func(context.Context) error, workDir string, requireTmpfs bool, commands map[string][]string) func(context.Context) error {
	return func(ctx context.Context) error {
		checkCtx, cancel := context.WithTimeout(ctx, readinessLimit)
		defer cancel()
		if databaseReady == nil || objectReady == nil || databaseReady(checkCtx) != nil || objectReady(checkCtx) != nil {
			return errors.New("dependency not ready")
		}
		if err := checkWorkDir(workDir, requireTmpfs); err != nil {
			return errors.New("dependency not ready")
		}
		if err := checkCommandVersions(checkCtx, commands); err != nil {
			return errors.New("dependency not ready")
		}
		return nil
	}
}

func healthHandler(ready func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /live", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if ready == nil || ready(r.Context()) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func checkWorkDir(path string, requireTmpfs bool) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("invalid work directory")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return errors.New("invalid work directory")
	}
	file, err := os.CreateTemp(path, ".ready-*")
	if err != nil {
		return errors.New("work directory is not writable")
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return errors.New("work directory is not writable")
	}
	if err := os.Remove(name); err != nil {
		return errors.New("work directory is not writable")
	}
	if requireTmpfs {
		mounts, err := os.Open("/proc/self/mountinfo")
		if err != nil {
			return errors.New("work directory is not tmpfs")
		}
		defer mounts.Close()
		if !pathOnTmpfs(path, mounts) {
			return errors.New("work directory is not tmpfs")
		}
	}
	return nil
}

func pathOnTmpfs(path string, reader io.Reader) bool {
	clean := filepath.Clean(path)
	bestLength := -1
	bestTmpfs := false
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := -1
		for i, field := range fields {
			if field == "-" {
				separator = i
				break
			}
		}
		if separator < 6 || separator+1 >= len(fields) {
			continue
		}
		mountPoint := strings.ReplaceAll(strings.ReplaceAll(fields[4], `\040`, " "), `\134`, `\`)
		mountPoint = filepath.Clean(mountPoint)
		if clean != mountPoint && !strings.HasPrefix(clean, mountPoint+string(filepath.Separator)) {
			continue
		}
		if len(mountPoint) > bestLength {
			bestLength = len(mountPoint)
			bestTmpfs = fields[separator+1] == "tmpfs"
		}
	}
	return scanner.Err() == nil && bestLength >= 0 && bestTmpfs
}

func checkCommandVersions(ctx context.Context, commands map[string][]string) error {
	if len(commands) == 0 {
		return errors.New("no processing commands configured")
	}
	for executable, args := range commands {
		if executable == "" || len(args) == 0 {
			return errors.New("invalid processing command")
		}
		command := exec.CommandContext(ctx, executable, args...)
		output := &boundedBuffer{remaining: commandOutputLimit}
		command.Stdout, command.Stderr = output, output
		if err := command.Run(); err != nil || output.exceeded || output.buf.Len() == 0 {
			return errors.New("processing command unavailable")
		}
	}
	return nil
}

type boundedBuffer struct {
	buf       bytes.Buffer
	remaining int
	exceeded  bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		b.exceeded = true
		if b.remaining > 0 {
			_, _ = b.buf.Write(p[:b.remaining])
			b.remaining = 0
		}
		return len(p), nil
	}
	b.remaining -= len(p)
	_, _ = b.buf.Write(p)
	return len(p), nil
}
