package pg

import (
	"errors"
	"io/fs"
	"unsafe"

	"gopgext/internal/core"
)

// Files of the server, e.g. to keep an extension's state across restarts.
// Relative paths are relative to the data directory (pg_stat/ is where
// statistics are kept). Unlike package os, which isn't available, these go
// through PostgreSQL (its file descriptor limits), and they never raise
// PostgreSQL errors: failures are returned as *fs.PathError, with
// fs.ErrNotExist for missing files.

//export gopgext_file_read
func fileRead(path *byte, data *unsafe.Pointer, n *int64, errno *int32) uintptr

//export gopgext_file_write
func fileWrite(path *byte, data *byte, n int64, errno *int32) uintptr

//export gopgext_file_remove
func fileRemove(path *byte) int32

//export gopgext_strerror
func strerror(errno int32) *byte

//export pfree
func pfree(p unsafe.Pointer)

const enoent = 2

func pathError(op, path string, errno int32) error {
	err := errors.New(core.GoString(strerror(errno)))
	if errno == enoent {
		err = fs.ErrNotExist
	}
	return &fs.PathError{Op: op, Path: path, Err: err}
}

// ReadFile returns the contents of a file.
func ReadFile(path string) ([]byte, error) {
	var p unsafe.Pointer
	var n int64
	var errno int32
	core.Check(fileRead(core.CBytes(path), &p, &n, &errno))
	if errno != 0 {
		return nil, pathError("read", path, errno)
	}
	b := append([]byte(nil), unsafe.Slice((*byte)(p), n)...)
	pfree(p)
	return b, nil
}

// WriteFile replaces a file with data atomically and durably: it writes a
// temporary file next to it (path + ".tmp"), flushes it to disk and renames
// it over path.
func WriteFile(path string, data []byte) error {
	var errno int32
	core.Check(fileWrite(core.CBytes(path), unsafe.SliceData(data), int64(len(data)), &errno))
	if errno != 0 {
		return pathError("write", path, errno)
	}
	return nil
}

// RemoveFile removes a file.
func RemoveFile(path string) error {
	if errno := fileRemove(core.CBytes(path)); errno != 0 {
		return pathError("remove", path, errno)
	}
	return nil
}

//export gopgext_user_id
func userID() uint32

//export gopgext_database_id
func databaseID() uint32

// UserID returns the current user (as SQL's current_user; the owner while a
// SECURITY DEFINER function runs).
func UserID() Oid { return Oid(userID()) }

// DatabaseID returns the database this backend is connected to (0 outside
// a database, e.g. in the postmaster).
func DatabaseID() Oid { return Oid(databaseID()) }

//export gopgext_has_privs_of
func hasPrivsOf(role *byte, out *bool) uintptr

// HasPrivilegesOf reports whether the current user has the privileges of
// a role: is a superuser, the role, or a member of it that inherits its
// privileges (false if the role doesn't exist). For example
// "pg_read_all_stats" for statistics about other users.
func HasPrivilegesOf(role string) bool {
	var ok bool
	core.Check(hasPrivsOf(core.CBytes(role), &ok))
	return ok
}

//export gopgext_preloading
func preloading() bool

// Preloading reports whether the library is being loaded by
// shared_preload_libraries, in the postmaster at server start (only true
// in main and package initialization). Libraries that need it (shared
// memory, hooks every backend must run) can check it in main.
func Preloading() bool { return preloading() }
