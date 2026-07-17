package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/config"
	"happylearn.local/app/internal/platform/database"
)

const maxPasswordFileBytes = 4096

type createTeacherArgs struct {
	Username     string
	DisplayName  string
	PasswordFile string
}
type dependencies struct {
	loadConfig func(func(string) string) (config.Config, error)
	open       func(context.Context, string) (*pgxpool.Pool, error)
	migrate    func(context.Context, *pgxpool.Pool) error
	newUsers   func(*pgxpool.Pool) auth.UserStore
	hash       func(string) (string, error)
	stdout     io.Writer
}

func main() {
	if err := run(context.Background(), os.Args[1:], productionDependencies()); err != nil {
		fmt.Fprintln(os.Stderr, "teacher bootstrap failed")
		os.Exit(1)
	}
}
func productionDependencies() dependencies {
	hasher := auth.NewPasswordHasher(auth.Argon2Params{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32})
	return dependencies{loadConfig: config.Load, open: database.Open, migrate: database.Migrate, newUsers: func(pool *pgxpool.Pool) auth.UserStore { return auth.NewPostgresUserStore(pool) }, hash: hasher.Hash, stdout: os.Stdout}
}
func run(ctx context.Context, args []string, deps dependencies) error {
	if len(args) == 0 || args[0] != "create-teacher" {
		return errors.New("usage: create-teacher requires named flags")
	}
	parsed, err := parseCreateTeacher(args[1:])
	if err != nil {
		return err
	}
	password, err := readPasswordFile(parsed.PasswordFile)
	if err != nil {
		return errors.New("invalid password file")
	}
	defer zero(password)
	if auth.ValidatePassword(string(password)) != nil {
		return errors.New("invalid password file")
	}
	cfg, err := deps.loadConfig(os.Getenv)
	if err != nil {
		return errors.New("load configuration")
	}
	pool, err := deps.open(ctx, cfg.DatabaseURL)
	if err != nil {
		return errors.New("open database")
	}
	defer pool.Close()
	if err := deps.migrate(ctx, pool); err != nil {
		return errors.New("migrate database")
	}
	hash, err := deps.hash(string(password))
	if err != nil {
		return errors.New("hash password")
	}
	_, err = deps.newUsers(pool).Create(ctx, auth.CreateUserParams{Username: parsed.Username, DisplayName: parsed.DisplayName, Role: auth.RoleAdmin, Status: auth.StatusActive, PasswordHash: hash, MustChangePassword: false})
	if errors.Is(err, auth.ErrConflict) {
		return errors.New("teacher administrator already exists")
	}
	if err != nil {
		return errors.New("create teacher")
	}
	if deps.stdout != nil {
		_, _ = fmt.Fprintln(deps.stdout, "teacher administrator created")
	}
	return nil
}
func parseCreateTeacher(args []string) (createTeacherArgs, error) {
	fs := flag.NewFlagSet("create-teacher", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var result createTeacherArgs
	fs.StringVar(&result.Username, "username", "", "")
	fs.StringVar(&result.DisplayName, "display-name", "", "")
	fs.StringVar(&result.PasswordFile, "password-file", "", "")
	if err := fs.Parse(args); err != nil {
		return createTeacherArgs{}, errors.New("invalid create-teacher flags")
	}
	if fs.NArg() != 0 || !validUsername(result.Username) || !validDisplayName(result.DisplayName) || strings.TrimSpace(result.PasswordFile) == "" {
		return createTeacherArgs{}, errors.New("invalid create-teacher flags")
	}
	return result, nil
}
func readPasswordFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("invalid password file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return nil, errors.New("invalid password file")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > maxPasswordFileBytes {
		return nil, errors.New("invalid password file")
	}
	data = []byte(strings.TrimSuffix(string(data), "\n"))
	data = []byte(strings.TrimSuffix(string(data), "\r"))
	if len(data) == 0 {
		return nil, errors.New("invalid password file")
	}
	return data, nil
}
func zero(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
func validUsername(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 3 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func validDisplayName(value string) bool {
	n := 0
	for _, r := range value {
		if r == '\uFFFD' {
			return false
		}
		n++
	}
	return n >= 1 && n <= 64 && strings.TrimSpace(value) != ""
}
