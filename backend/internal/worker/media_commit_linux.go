package worker

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func renameMediaFile(temporary, final string) error {
	err := unix.Renameat2(unix.AT_FDCWD, temporary, unix.AT_FDCWD, final, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return linkMediaFile(temporary, final)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: temporary, New: final, Err: err}
	}
	return nil
}
