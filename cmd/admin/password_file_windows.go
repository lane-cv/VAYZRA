//go:build windows

package main

import "errors"

// Windows reparse-point and POSIX mode semantics cannot be proven with the
// standard library handle API used by this binary, so fail closed rather than
// following a potentially substituted password-file path.
func readPasswordFile(string) ([]byte, error) { return nil, errors.New("invalid password file") }
