package secretfile

import (
	"bytes"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const MaxBytes = 64 * 1024

var ErrInvalid = errors.New("invalid secret file")

var ErrConflict = errors.New("conflicting secret sources")

type Lookup func(string) (string, bool)

// Resolve returns NAME directly, or reads NAME_FILE through the same
// descriptor-based checks as Read. Errors intentionally contain neither the
// variable name, file path, nor secret value.
func Resolve(lookup Lookup, name string, maxBytes int64) (string, error) {
	if lookup == nil || name == "" || maxBytes <= 0 {
		return "", ErrInvalid
	}
	direct, directOK := lookup(name)
	path, fileOK := lookup(name + "_FILE")
	if directOK && fileOK {
		return "", ErrConflict
	}
	if directOK {
		return direct, nil
	}
	if !fileOK {
		return "", nil
	}
	value, err := read(path, maxBytes)
	if err != nil {
		return "", err
	}
	return string(value), nil
}

func Read(path string) ([]byte, error) {
	return read(path, MaxBytes)
}

func read(path string, maxBytes int64) ([]byte, error) {
	if path == "" {
		return nil, ErrInvalid
	}
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, ErrInvalid
	}
	file := os.NewFile(uintptr(fd), "secret")
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrInvalid
	}
	defer file.Close()

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil ||
		stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		(stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) ||
		stat.Mode&0o022 != 0 ||
		stat.Size > maxBytes {
		return nil, ErrInvalid
	}

	body, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(body)) > maxBytes {
		return nil, ErrInvalid
	}
	if bytes.HasSuffix(body, []byte("\r\n")) {
		body = body[:len(body)-2]
	} else if bytes.HasSuffix(body, []byte("\n")) {
		body = body[:len(body)-1]
	}
	if len(body) == 0 {
		return nil, ErrInvalid
	}
	return body, nil
}
