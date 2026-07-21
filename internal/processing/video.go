package processing

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

const maxVideoDurationSeconds = 12 * 60 * 60

type VideoProbe struct {
	Container, Codec string
	DurationMS       int64
	Width, Height    int
	BrowserPlayable  bool
}
type VideoProber struct {
	Runner     Runner
	Executable string
}

func (p VideoProber) Probe(ctx context.Context, path string) (VideoProbe, error) {
	if p.Runner == nil || path == "" {
		return VideoProbe{}, reject("video_rejected")
	}
	executable := p.Executable
	if executable == "" {
		executable = "ffprobe"
	}
	stdout, _, exit, err := p.Runner.Run(ctx, executable, []string{"-v", "error", "-show_entries", "format=format_name,duration:stream=codec_type,codec_name,width,height", "-of", "json", "--", path}, 1024*1024, 64*1024)
	if err != nil || exit != 0 {
		return VideoProbe{}, transient("probe_failed")
	}
	var payload struct {
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if json.Unmarshal(stdout, &payload) != nil {
		return VideoProbe{}, reject("video_rejected")
	}
	duration, err := strconv.ParseFloat(payload.Format.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration < 0 || duration > maxVideoDurationSeconds {
		return VideoProbe{}, reject("video_rejected")
	}
	var video *struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	}
	for i := range payload.Streams {
		if payload.Streams[i].CodecType == "video" {
			if video != nil {
				return VideoProbe{}, reject("video_rejected")
			}
			video = &payload.Streams[i]
		}
	}
	if video == nil || video.Width < 1 || video.Width > 7680 || video.Height < 1 || video.Height > 4320 {
		return VideoProbe{}, reject("video_rejected")
	}
	container := normalizeContainer(payload.Format.FormatName, video.CodecName)
	playable := browserPlayable(container, video.CodecName)
	return VideoProbe{Container: container, Codec: video.CodecName, DurationMS: int64(math.Round(duration * 1000)), Width: video.Width, Height: video.Height, BrowserPlayable: playable}, nil
}
func normalizeContainer(value, codec string) string {
	v := strings.ToLower(value)
	switch {
	case strings.Contains(v, "mp4") || strings.Contains(v, "mov,"):
		return "mp4"
	case strings.Contains(v, "webm") && browserPlayable("webm", codec):
		return "webm"
	case strings.Contains(v, "matroska"):
		return "mkv"
	case strings.Contains(v, "ogg"):
		return "ogg"
	case strings.Contains(v, "avi"):
		return "avi"
	default:
		return strings.Split(v, ",")[0]
	}
}
func browserPlayable(container, codec string) bool {
	switch container {
	case "mp4":
		return codec == "h264" || codec == "av1"
	case "webm":
		return codec == "vp8" || codec == "vp9" || codec == "av1"
	case "ogg":
		return codec == "theora"
	}
	return false
}
