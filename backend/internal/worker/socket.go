package worker

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// The persistent lock inode is intentionally never unlinked: removing it could
// allow a third process to lock a different inode while a waiter owns this one.
type ownedSocket struct {
	conn *net.UnixConn
	lock *os.File
	path string
	info os.FileInfo
}

func sameUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}
func ownSocket(path string) (*ownedSocket, error) {
	if path == "" {
		return nil, errors.New("wake socket path is required")
	}
	dir := filepath.Dir(path)
	// Reject symlink ancestors before creating any state, not just the final
	// directory. Only the immediate directory may be absent.
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for p := absolute; ; p = filepath.Dir(p) {
		i, e := os.Lstat(p)
		if p == absolute && os.IsNotExist(e) {
			continue
		}
		if e != nil || i.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("socket directory contains a symlink")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, errors.New("cannot create private socket directory")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || !sameUser(info) {
		return nil, errors.New("socket directory must be private and same-user")
	}
	fd, err := syscall.Open(path+".lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, errors.New("cannot open socket lock")
	}
	lock := os.NewFile(uintptr(fd), path+".lock")
	good := false
	defer func() {
		if !good {
			lock.Close()
		}
	}()
	li, err := lock.Stat()
	if err != nil || !li.Mode().IsRegular() || !sameUser(li) || li.Mode().Perm()&0077 != 0 {
		return nil, errors.New("unsafe socket lock")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.New("wake socket already owned")
	}
	if old, err := os.Lstat(path); err == nil {
		if old.Mode()&os.ModeSocket == 0 || !sameUser(old) {
			return nil, errors.New("refusing to replace non-owned socket")
		}
		// The exclusive lock excludes other conforming workers. A socket that is
		// still accepting datagrams belongs to an active nonconforming process.
		c, e := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
		if e == nil {
			c.Close()
			return nil, errors.New("wake socket is active")
		}
		if err := os.Remove(path); err != nil {
			return nil, errors.New("cannot remove stale socket")
		}
	} else if !os.IsNotExist(err) {
		return nil, errors.New("cannot inspect wake socket")
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return nil, errors.New("cannot bind wake socket")
	}
	info, err = os.Lstat(path)
	if err != nil {
		conn.Close()
		return nil, errors.New("cannot inspect bound socket")
	}
	if err = os.Chmod(path, 0600); err != nil {
		conn.Close()
		if current, e := os.Lstat(path); e == nil && os.SameFile(info, current) {
			_ = os.Remove(path)
		}
		return nil, errors.New("cannot protect wake socket")
	}
	good = true
	return &ownedSocket{conn: conn, lock: lock, path: path, info: info}, nil
}
func (s *ownedSocket) close() {
	s.conn.Close()
	if current, err := os.Lstat(s.path); err == nil && os.SameFile(s.info, current) {
		_ = os.Remove(s.path)
	}
	s.lock.Close()
}
