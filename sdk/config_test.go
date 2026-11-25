package sdk

import (
	"encoding/json"
	"testing"
)

func TestParseDrScene(t *testing.T) {
	mk := func(s string) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if parseDrScene(mk(`{}`)) {
		t.Fatal("absent key must be false (not bucketed into the experiment)")
	}
	if !parseDrScene(mk(`{"video_player_api_dr_scene":{"enable":true}}`)) {
		t.Fatal("object form enable:true must parse")
	}
	if parseDrScene(mk(`{"video_player_api_dr_scene":{"enable":false}}`)) {
		t.Fatal("object form enable:false must parse")
	}
	if !parseDrScene(mk(`{"video_player_api_dr_scene":"{\"enable\":true}"}`)) {
		t.Fatal("string form enable:true must parse")
	}
	if parseDrScene(mk(`{"video_player_api_dr_scene":"not json"}`)) {
		t.Fatal("garbage must be false")
	}
}
