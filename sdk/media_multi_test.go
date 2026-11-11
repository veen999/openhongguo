package sdk

// Unit tests for the multi playback channels (2026-11-11): response
// extraction from data.video_model_data[<episode>] and the request
// shapes. No network.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/beilunyang/openhongguo/internal/protocol"
)

const multiModelObj = `{"status":10,"video_id":"v-multi","video_list":{"540p":{"main_url":"https://media.invalid/540.mp4","video_meta":{"definition":"540p","vheight":960}}}}`

func TestParseMultiPlaybackVideoModelData(t *testing.T) {
	modelStr, _ := json.Marshal(multiModelObj)
	raw := []byte(`{"code":0,"data":{"video_model_data":{"7699003626647194649":{"video_model":` + string(modelStr) + `}}}}`)
	pb, err := parseMultiPlayback(raw, "7699003626647194649")
	if err != nil {
		t.Fatalf("parseMultiPlayback: %v", err)
	}
	if pb == nil || len(pb.Streams) == 0 {
		t.Fatalf("expected streams, got %+v", pb)
	}
	if got := pb.Streams[0].URL; got != "https://media.invalid/540.mp4" {
		t.Fatalf("stream URL = %q", got)
	}
}

func TestParseMultiPlaybackMissingEpisode(t *testing.T) {
	raw := []byte(`{"code":0,"data":{"video_model_data":{"111":{"video_model":"{}"}}}}`)
	if _, err := parseMultiPlayback(raw, "7699003626647194649"); err == nil {
		t.Fatal("expected error for missing episode entry")
	}
}

func TestMultiPlaybackBodyShape(t *testing.T) {
	body, err := protocol.MultiPlaybackBody("7699003626647194649")
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		BizParam map[string]any `json:"biz_param"`
		Mixed    map[string]any `json:"mixed_video_id_map"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.BizParam["video_platform"] != float64(1024) {
		t.Fatalf("video_platform = %v", decoded.BizParam["video_platform"])
	}
	if decoded.BizParam["need_all_video_definition"] != true {
		t.Fatalf("need_all_video_definition = %v", decoded.BizParam["need_all_video_definition"])
	}
	ids, ok := decoded.Mixed["1004"].([]any)
	if !ok || len(ids) != 1 || ids[0] != "7699003626647194649" {
		t.Fatalf("mixed_video_id_map = %v", decoded.Mixed)
	}
}

func TestTabMultiPairsShape(t *testing.T) {
	id := protocol.SearchIdentity{DeviceID: "123", InstallID: "456", CDID: "cdid", OpenUDID: "ouid"}
	pairs := protocol.SignedVideoTabMultiPairs(id, "7699003626647194649")
	joined := ""
	byKey := map[string]string{}
	for _, p := range pairs {
		joined += p[0] + "=" + p[1] + "&"
		byKey[p[0]] = p[1]
	}
	for key, want := range map[string]string{
		"video_id": "7699003626647194649", "video_id_type": "0", "device_level": "1",
		"need_all_video_definition": "1", "need_mp4_align": "0", "use_os_player": "0",
		"video_platform": "1024", "source": "0",
		"version_code": protocol.TabVersionCode, "version_name": protocol.TabVersionName,
	} {
		if byKey[key] != want {
			t.Fatalf("pair %s = %q, want %q (all: %s)", key, byKey[key], want, joined)
		}
	}
	if _, ok := byKey["openudid"]; ok {
		t.Fatalf("playback pairs must not carry openudid: %s", joined)
	}
	if !strings.HasPrefix(joined, "ac=wifi&") || !strings.Contains(joined, "&device_id=123&iid=456&cdid=cdid&video_id=") {
		t.Fatalf("pairs should be device fields, then device_id/iid/cdid, then video params: %s", joined)
	}
}
