#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
EMBY_FFMPEG_ROOT="${EMBY_FFMPEG_ROOT:-/opt/emby-server}"
FFMPEG="${EMBY_FFMPEG_ROOT}/bin/emby-ffmpeg"
FFPROBE_BINARY="${EMBY_FFMPEG_ROOT}/bin/ffprobe"
LOADER="${EMBY_FFMPEG_ROOT}/lib/ld-linux-x86-64.so.2"
LIBRARIES="${EMBY_FFMPEG_ROOT}/lib:${EMBY_FFMPEG_ROOT}/extra/lib"
ASS_FIXTURE="${ROOT_DIR}/scripts/acceptance/fixtures/sample.ass"
SRT_FIXTURE="${ROOT_DIR}/scripts/acceptance/fixtures/sample.srt"

for file in "${FFMPEG}" "${FFPROBE_BINARY}" "${LOADER}" "${ASS_FIXTURE}" "${SRT_FIXTURE}"; do
  test -r "${file}"
done

work="$(mktemp -d /tmp/emby-auto-media-acceptance.XXXXXX)"
trap 'rm -rf "${work}"' EXIT

probe() {
  LD_LIBRARY_PATH="${LIBRARIES}" "${LOADER}" "${FFPROBE_BINARY}" "$@"
}

"${FFMPEG}" -hide_banner -loglevel error -y \
  -f lavfi -i testsrc2=size=640x360:rate=24 \
  -f lavfi -i sine=frequency=880:sample_rate=48000 \
  -i "${ASS_FIXTURE}" -t 2 \
  -map 0:v:0 -map 1:a:0 -map 2:0 \
  -c:v libx264 -preset ultrafast -crf 30 -pix_fmt yuv420p -c:a aac -c:s ass \
  "${work}/source-embedded.mkv"

"${FFMPEG}" -hide_banner -loglevel error -y -i "${work}/source-embedded.mkv" \
  -map 0:v:0 -map 0:a:0 -c:v libx264 -preset ultrafast -crf 30 \
  -pix_fmt yuv420p -c:a aac "${work}/profile-h264.mp4"

"${FFMPEG}" -hide_banner -loglevel error -y -i "${work}/source-embedded.mkv" \
  -map 0:v:0 -map 0:a:0 -c:v libx265 -preset ultrafast -crf 32 \
  -x265-params log-level=error -pix_fmt yuv420p -c:a copy "${work}/profile-hevc.mkv"

"${FFMPEG}" -hide_banner -loglevel error -y -i "${work}/source-embedded.mkv" \
  -map 0:s:0 -c:s ass "${work}/embedded.ass"
"${FFMPEG}" -hide_banner -loglevel error -y -i "${SRT_FIXTURE}" \
  -c:s ass "${work}/external.ass"

source_streams="$(probe -v error -show_entries stream=codec_type,codec_name -of compact=p=0:nk=1 "${work}/source-embedded.mkv")"
h264_probe="$(probe -v error -select_streams v:0 -show_entries stream=codec_name,pix_fmt -of csv=p=0 "${work}/profile-h264.mp4")"
hevc_probe="$(probe -v error -select_streams v:0 -show_entries stream=codec_name,pix_fmt -of csv=p=0 "${work}/profile-hevc.mkv")"

grep -q 'h264|video' <<<"${source_streams}"
grep -q 'aac|audio' <<<"${source_streams}"
grep -q 'ass|subtitle' <<<"${source_streams}"
grep -q '^h264,yuv420p' <<<"${h264_probe}"
grep -q '^hevc,yuv420p' <<<"${hevc_probe}"
grep -q 'Emby Auto acceptance subtitle' "${work}/embedded.ass"
grep -q 'Emby Auto external subtitle' "${work}/external.ass"

printf 'source_streams=%s\nh264=%s\nhevc=%s\n' \
  "$(tr '\n' ',' <<<"${source_streams}" | sed 's/,$//')" "${h264_probe}" "${hevc_probe}"
stat -c '%n %s bytes' "${work}"/*

for concurrency in 1 2 4; do
  started="$(date +%s%N)"
  pids=()
  for index in $(seq 1 "${concurrency}"); do
    output="${work}/concurrency-${concurrency}-${index}.mp4"
    "${FFMPEG}" -hide_banner -loglevel error -y -i "${work}/source-embedded.mkv" \
      -map 0:v:0 -an -c:v libx264 -preset ultrafast -crf 32 -pix_fmt yuv420p "${output}" &
    pids+=("$!")
  done
  for pid in "${pids[@]}"; do
    wait "${pid}"
  done
  for index in $(seq 1 "${concurrency}"); do
    output="${work}/concurrency-${concurrency}-${index}.mp4"
    [[ "$(probe -v error -select_streams v:0 -show_entries stream=codec_name,pix_fmt -of csv=p=0 "${output}")" == "h264,yuv420p" ]]
    rm -f "${output}"
  done
  elapsed_ms=$((($(date +%s%N) - started) / 1000000))
  printf 'parallel_transcodes=%d elapsed_ms=%d\n' "${concurrency}" "${elapsed_ms}"
done
