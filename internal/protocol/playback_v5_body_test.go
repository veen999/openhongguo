package protocol

import (
	"encoding/json"
	"testing"
)

// PlaybackVideoModelBodyFor 必须把 content_type 原样写进请求体：
// PUGC(1001) 等类型上游要求与单集一致，写死 1 会被回 101000
// （2026-11-26 实测）。
func TestPlaybackVideoModelBodyForContentType(t *testing.T) {
	for _, tc := range []struct {
		name string
		ct   int
		want int
	}{
		{"pugc", 1001, 1001},
		{"motion comic", 1004, 1004},
		{"short series", 1, 1},
		{"zero falls back", 0, 1},
		{"negative falls back", -3, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := PlaybackVideoModelBodyFor("7706480472527752216", tc.ct)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				ContentType int    `json:"content_type"`
				VideoID     string `json:"video_id"`
				BizParam    struct {
					VideoPlatform int `json:"video_platform"`
				} `json:"biz_param"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if body.ContentType != tc.want {
				t.Fatalf("content_type = %d, want %d", body.ContentType, tc.want)
			}
			if body.VideoID != "7706480472527752216" || body.BizParam.VideoPlatform != 1024 {
				t.Fatalf("body drifted: %s", raw)
			}
		})
	}
	// 默认版式仍是 1。
	raw, err := PlaybackVideoModelBody("7706480472527752216")
	if err != nil {
		t.Fatal(err)
	}
	var def struct {
		ContentType int `json:"content_type"`
	}
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatal(err)
	}
	if def.ContentType != 1 {
		t.Fatalf("default content_type = %d, want 1", def.ContentType)
	}
}
