package processing

import (
	"context"
	"testing"
)

func TestVideoProberAcceptsBoundedBrowserVideo(t *testing.T) {
	runner := &runnerStub{stdout: []byte(`{"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"61.250"},"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080}]}`)}
	result, err := (VideoProber{Runner: runner, Executable: "ffprobe"}).Probe(context.Background(), "/work/input")
	if err != nil || result.Container != "mp4" || result.Codec != "h264" || result.DurationMS != 61250 || !result.BrowserPlayable {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestVideoProberMarksDownloadOnlyAndRejectsUnsafeMetadata(t *testing.T) {
	runner := &runnerStub{stdout: []byte(`{"format":{"format_name":"matroska,webm","duration":"10"},"streams":[{"codec_type":"video","codec_name":"hevc","width":1920,"height":1080}]}`)}
	result, err := (VideoProber{Runner: runner}).Probe(context.Background(), "/work/input")
	if err != nil || result.BrowserPlayable {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, payload := range []string{`{"format":{"format_name":"mp4","duration":"50000"},"streams":[{"codec_type":"video","codec_name":"h264","width":1,"height":1}]}`, `{"format":{"format_name":"mp4","duration":"1"},"streams":[{"codec_type":"video","codec_name":"h264","width":8000,"height":1}]}`, `not json`} {
		runner.stdout = []byte(payload)
		if _, err = (VideoProber{Runner: runner}).Probe(context.Background(), "/work/input"); category(err) != "video_rejected" {
			t.Fatalf("payload=%q err=%v", payload, err)
		}
	}
}
