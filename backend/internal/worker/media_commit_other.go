//go:build !linux

package worker

func renameMediaFile(temporary, final string) error {
	return linkMediaFile(temporary, final)
}
