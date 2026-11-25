package sdk

import (
	"testing"

	"github.com/beilunyang/openhongguo/sdk/model"
)

// 竖屏 1080×1920 在接口里标作 "1080p"：标签必须取短边，
// 不能按高度报成 1920P（用户报障 2026-11-25）。
func TestMaxDefinitionLabelUsesShortSide(t *testing.T) {
	pb := &model.Playback{Streams: []model.Stream{
		{Definition: "720p", Width: 720, Height: 1280},
		{Definition: "1080p", Width: 1080, Height: 1920},
	}}
	if got := maxDefinitionLabel(pb); got != "1080P" {
		t.Fatalf("portrait ladder: got %q, want 1080P", got)
	}
	landscape := &model.Playback{Streams: []model.Stream{
		{Definition: "1080p", Width: 1920, Height: 1080},
	}}
	if got := maxDefinitionLabel(landscape); got != "1080P" {
		t.Fatalf("landscape ladder: got %q, want 1080P", got)
	}
	labelOnly := &model.Playback{Streams: []model.Stream{
		{Definition: "720p"},
	}}
	if got := maxDefinitionLabel(labelOnly); got != "720P" {
		t.Fatalf("label-only ladder: got %q, want 720P", got)
	}
}
