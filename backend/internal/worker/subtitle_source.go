package worker

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

const (
	maxSubtitleSourceBytes     = 64 << 20
	subtitleCharsetSampleBytes = 256 << 10
)

// prepareSubtitleSource 把外挂文本字幕统一转换为 UTF-8，供 FFmpeg 与内容检查使用。
// UTF-8 源直接复用原路径；其它编码写入 stagingDirectory 下的临时文件，
// 调用方必须执行返回的清理函数。
func prepareSubtitleSource(sourcePath, stagingDirectory string) (string, func(), error) {
	content, err := readSubtitleSourceFile(sourcePath)
	if err != nil {
		return "", nil, err
	}
	sample := content
	if len(sample) > subtitleCharsetSampleBytes {
		sample = sample[:subtitleCharsetSampleBytes]
	}
	charset, err := domain.DetectSubtitleCharset(sample, filepath.Base(sourcePath))
	if err != nil {
		return "", nil, err
	}
	if charset == domain.SubtitleCharsetUTF8 {
		return sourcePath, func() {}, nil
	}
	decoded, err := domain.DecodeSubtitleText(content, charset)
	if err != nil {
		return "", nil, err
	}
	// 使用带操作 UUID 的 .part 临时命名，与媒体临时文件保持一致。
	temporaryPath := filepath.Join(stagingDirectory, ".subtitle-source."+uuid.NewString()+".part"+filepath.Ext(sourcePath))
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return "", nil, fmt.Errorf("create converted subtitle source: %w", err)
	}
	cleanup := func() { _ = os.Remove(temporaryPath) }
	if _, err := file.Write(decoded); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, fmt.Errorf("write converted subtitle source: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close converted subtitle source: %w", err)
	}
	return temporaryPath, cleanup, nil
}

func readSubtitleSourceFile(filePath string) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() <= 0 || info.Size() > maxSubtitleSourceBytes {
		return nil, fmt.Errorf("subtitle source size must be between 1 and %d bytes", maxSubtitleSourceBytes)
	}
	content, err := io.ReadAll(io.LimitReader(file, maxSubtitleSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxSubtitleSourceBytes {
		return nil, fmt.Errorf("subtitle source exceeds %d bytes", maxSubtitleSourceBytes)
	}
	return content, nil
}
