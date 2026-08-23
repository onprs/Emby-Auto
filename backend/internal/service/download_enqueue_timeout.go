package service

import "time"

// DownloadEnqueueTimeout 为所有 download.enqueue 调度入口的统一超时预算。
// 新路径顺序预算：torrent 拉取 30s + qB 添加/确认 15s + 元数据等待 90s = 135s，
// 加上网络/数据库开销需至少 3m，避免在合法慢路径未完成前被外层操作取消。
const DownloadEnqueueTimeout = 3 * time.Minute
