package secretfile

import (
	"bytes"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const MaxBytes = 8 * 1024

var ErrInvalid = errors.New("invalid secret file")

func Read(path string) ([]byte, error) {
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
		stat.Size > MaxBytes {
		return nil, ErrInvalid
	}

	body, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil || len(body) > MaxBytes {
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
