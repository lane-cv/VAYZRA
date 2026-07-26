package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const (
	controlAddress = ":8092"
	stopTimeout    = 20 * time.Second
)

type workerController struct {
	mu      sync.Mutex
	command *exec.Cmd
	held    bool
	done    chan struct{}
}

func startWorker(command *exec.Cmd) (*workerController, error) {
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return nil, err
	}
	controller := &workerController{command: command, done: make(chan struct{})}
	go func() {
		_ = command.Wait()
		close(controller.done)
	}()
	return controller, nil
}

func (c *workerController) signal(signal syscall.Signal) error {
	select {
	case <-c.done:
		return errors.New("worker already exited")
	default:
	}
	return syscall.Kill(-c.command.Process.Pid, signal)
}

func (c *workerController) hold() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held {
		return nil
	}
	if err := c.signal(syscall.SIGSTOP); err != nil {
		return err
	}
	c.held = true
	return nil
}

func (c *workerController) release() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.held {
		return nil
	}
	if err := c.signal(syscall.SIGCONT); err != nil {
		return err
	}
	c.held = false
	return nil
}

func (c *workerController) isHeld() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held
}

func (c *workerController) stop(timeout time.Duration) error {
	_ = c.release()
	select {
	case <-c.done:
		return nil
	default:
	}
	if err := c.signal(syscall.SIGTERM); err != nil {
		return err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-c.done:
		return nil
	case <-timer.C:
		_ = c.signal(syscall.SIGKILL)
		<-c.done
		return errors.New("worker required forced shutdown")
	}
}

func controlHandler(controller *workerController, requiredToken string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	change := func(held bool) http.HandlerFunc {
		return func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodPost {
				http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			token := request.Header.Get("X-E2E-Control-Token")
			if requiredToken == "" || len(token) != len(requiredToken) ||
				subtle.ConstantTimeCompare([]byte(token), []byte(requiredToken)) != 1 {
				http.NotFound(writer, request)
				return
			}
			var err error
			if held {
				err = controller.hold()
			} else {
				err = controller.release()
			}
			if err != nil {
				http.Error(writer, "worker control unavailable", http.StatusServiceUnavailable)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(writer).Encode(map[string]bool{"held": controller.isHeld()})
		}
	}
	mux.HandleFunc("/hold", change(true))
	mux.HandleFunc("/release", change(false))
	return mux
}

func shutdownWorker(_ context.Context, controller *workerController) error {
	return controller.stop(stopTimeout)
}

func run() error {
	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	controlToken := os.Getenv("E2E_AI_PROCESSING_CONTROL_TOKEN")
	if controlToken == "" {
		return errors.New("processing control token required")
	}

	controller, err := startWorker(exec.Command("/app/happylearn-worker"))
	if err != nil {
		return err
	}
	releaseWorker := func() { _ = controller.release() }
	defer releaseWorker()

	server := &http.Server{
		Addr:              controlAddress,
		Handler:           controlHandler(controller, controlToken),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       15 * time.Second,
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ListenAndServe() }()

	select {
	case <-shutdownContext.Done():
	case <-controller.done:
		return errors.New("worker exited")
	case err := <-serverDone:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}

	serverContext, cancelServer := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelServer()
	_ = server.Shutdown(serverContext)
	return shutdownWorker(shutdownContext, controller)
}

func main() {
	if err := run(); err != nil {
		log.Print("e2e_processing_supervisor_error")
		os.Exit(1)
	}
}
