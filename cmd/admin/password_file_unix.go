//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly || solaris || aix

package main

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// readPasswordFile opens and verifies one descriptor. The bounded buffer is
// deliberately kept as bytes; run converts it once for the legacy hasher API
// and zeroes this slice after hashing.
func readPasswordFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("invalid password file")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, errors.New("invalid password file")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 {
		_ = unix.Close(fd)
		return nil, errors.New("invalid password file")
	}
	file := os.NewFile(uintptr(fd), "password-file")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("invalid password file")
	}
	defer file.Close()
	data := make([]byte, maxPasswordFileBytes+1)
	n := 0
	for n < len(data) {
		readN, readErr := file.Read(data[n:])
		n += readN
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, errors.New("invalid password file")
		}
		if readN == 0 {
			break
		}
	}
	if n == len(data) {
		return nil, errors.New("invalid password file")
	}
	data = data[:n]
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}
	if len(data) > 0 && data[len(data)-1] == '\r' {
		data = data[:len(data)-1]
	}
	if len(data) == 0 {
		return nil, errors.New("invalid password file")
	}
	return data, nil
}
