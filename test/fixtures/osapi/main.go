// osapi checks that the standard library's operating system packages work
// inside a backend: files and directories, the working directory (the data
// directory), the environment, process ids and errors from the kernel.
package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RoundTrip writes data to a new file in a temporary directory, reads it
// back, lists the directory and removes it.
//
//gopgext:function
func RoundTrip(data string) (string, error) {
	dir, err := os.MkdirTemp("", "gopgext-osapi-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		return "", err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString("!"); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if st.Size() != int64(len(data)+1) || st.Mode().Perm() != 0o600 {
		return "", errors.New("unexpected stat: " + st.Mode().String())
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(entries) != 1 || entries[0].Name() != "data.txt" {
		return "", errors.New("unexpected directory listing")
	}
	return string(b), nil
}

// DataDirFiles lists the entries of the backend's working directory (the
// data directory) that match the given names.
//
//gopgext:function
func DataDirFiles(names []string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	var found []string
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(wd, n)); err == nil {
			found = append(found, n)
		}
	}
	sort.Strings(found)
	return strings.Join(found, ","), nil
}

// Missing reports whether opening a nonexistent file fails with
// fs.ErrNotExist.
//
//gopgext:function
func Missing() bool {
	_, err := os.Open("/nonexistent/gopgext-osapi")
	return errors.Is(err, fs.ErrNotExist)
}

// Pid is the backend's process id.
//
//gopgext:function
func Pid() int32 { return int32(os.Getpid()) }

// Env returns an environment variable ("" when unset).
//
//gopgext:function
func Env(name string) string { return os.Getenv(name) }

// SetEnv sets an environment variable and reads it back.
//
//gopgext:function
func SetEnv(name, value string) (string, error) {
	if err := os.Setenv(name, value); err != nil {
		return "", err
	}
	return os.Getenv(name), nil
}

func main() {}
