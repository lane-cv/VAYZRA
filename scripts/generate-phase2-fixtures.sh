#!/usr/bin/env bash
set -Eeuo pipefail

destination="${1:-}"
if [[ -z "$destination" || "$destination" != /* || "$destination" == "/" ]]; then
  echo "usage: $0 /absolute/disposable-fixture-directory" >&2
  exit 2
fi
for command in ffmpeg soffice dd; do
  command -v "$command" >/dev/null || { echo "missing fixture tool: $command" >&2; exit 1; }
done

install -d -m 0700 "$destination"
work="$(mktemp -d)"
cleanup() { rm -rf "$work"; }
trap cleanup EXIT
install -d -m 0700 "$work/home" "$work/cache"
export HOME="$work/home"
export XDG_CACHE_HOME="$work/cache"

printf '%s\n' '<html><body><h1>HappyLearn lesson</h1><p>Safe DOCX preview fixture.</p></body></html>' > "$work/lesson.html"
printf '%s\n' '<html><body><h1>HappyLearn replacement</h1><p>Second safe version.</p></body></html>' > "$work/replacement.html"
profile_url="file://$work/libreoffice-profile"
soffice --headless --nologo --nodefault --nofirststartwizard "-env:UserInstallation=$profile_url" --convert-to 'docx:Office Open XML Text' --outdir "$destination" "$work/lesson.html" >/dev/null
soffice --headless --nologo --nodefault --nofirststartwizard "-env:UserInstallation=$profile_url" --convert-to 'docx:Office Open XML Text' --outdir "$destination" "$work/replacement.html" >/dev/null

ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=640x360:rate=30:duration=30 -f lavfi -i sine=frequency=440:duration=30 \
  -c:v libx264 -preset ultrafast -b:v 4M -maxrate 4M -bufsize 8M -pix_fmt yuv420p -c:a aac -movflags +faststart -y "$destination/lesson.mp4"
ffmpeg -hide_banner -loglevel error -f lavfi -i color=c=red:s=160x90:d=1 -c:v ffv1 -y "$destination/unsupported.mkv"

dd if=/dev/zero of="$destination/resume.bin" bs=1M count=9 status=none
cp "$destination/lesson.docx" "$destination/archive.zip"
cp "$destination/lesson.docx" "$destination/macro.docm"
printf '%s\n' 'this is deliberately not a PDF' > "$destination/mismatch.pdf"

# Assemble the standard harmless antivirus validation probe only at runtime so
# neither the signature nor a generated malware fixture is stored in Git.
probe_a='X5O!P%@AP[4\PZX54(P^)7CC)7}'
probe_b='$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*'
printf '%s%s' "$probe_a" "$probe_b" > "$destination/eicar.txt"

chmod 0600 "$destination"/*
test "$(wc -c < "$destination/resume.bin")" -gt 8388608
