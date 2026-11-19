package sdk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 批量 v5 响应是平铺布局：data[<episode>] 直接挂每集的条目（2026-11-19
// 实测）。用单集夹具的条目拼一个双集平铺响应，验证解析与缺失回退名单。
func TestParseBatchV5PlaybackFlattened(t *testing.T) {
	rawFixture, err := os.ReadFile(filepath.Join("..", "testdata", "protocol", "drm-playback-v5.json"))
	if err != nil {
		t.Fatal(err)
	}
	var single struct {
		Data map[string]map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rawFixture, &single); err != nil {
		t.Fatal(err)
	}
	entry := single.Data["1004"]["7100000000000201"]
	if len(entry) == 0 {
		t.Fatal("fixture entry missing")
	}
	batch, err := json.Marshal(map[string]any{
		"code": 0,
		"data": map[string]json.RawMessage{
			"7100000000000201": entry,
			"7100000000000202": entry,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, missing := parseBatchV5Playback(batch, []string{"7100000000000201", "7100000000000202", "7100000000000203"})
	if len(got) != 2 {
		t.Fatalf("got %d playbacks, want 2", len(got))
	}
	for id, pb := range got {
		if len(pb.Streams) == 0 {
			t.Fatalf("episode %s parsed with no streams", id)
		}
	}
	if len(missing) != 1 || missing[0] != "7100000000000203" {
		t.Fatalf("missing = %v, want [7100000000000203]", missing)
	}
	// 空响应：全部回退。
	got, missing = parseBatchV5Playback([]byte(`{"code":0,"data":{}}`), []string{"7100000000000201"})
	if len(got) != 0 || len(missing) != 1 {
		t.Fatalf("empty response: got %d missing %v", len(got), missing)
	}
}
