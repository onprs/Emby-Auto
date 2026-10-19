package maintenance

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func privateFileInfo(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("维护快照必须是私有普通文件")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("维护快照所有者不匹配")
	}
	return nil
}

func WritePrivateJSON(path string, value any) error {
	if !filepath.IsAbs(path) {
		return errors.New("维护快照必须使用绝对路径")
	}
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("维护快照目录必须预先创建且仅当前账户可访问")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("维护快照目录所有者不匹配")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return errors.New("无法打开维护快照目录")
	}
	defer func() { _ = root.Close() }()
	file, err := root.OpenFile(filepath.Base(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("维护快照已存在或不可创建")
	}
	defer func() { _ = file.Close() }()
	if err := json.NewEncoder(file).Encode(value); err != nil {
		return errors.New("无法写入维护快照")
	}
	if err := file.Sync(); err != nil {
		return errors.New("无法持久化维护快照")
	}
	directory, err := root.Open(".")
	if err != nil {
		return errors.New("无法打开维护快照目录")
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return errors.New("无法持久化维护快照目录")
	}
	return nil
}

func ReadPrivateJSON(path string, value any) error {
	if err := privateFileInfo(path); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("无法读取维护快照")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("打开的维护快照权限无效")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("打开的维护快照所有者不匹配")
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errors.New("维护快照格式无效")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("维护快照包含多余内容")
	}
	return nil
}
