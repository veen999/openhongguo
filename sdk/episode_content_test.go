package sdk

import (
	"errors"
	"testing"

	"github.com/beilunyang/openhongguo/sdk/model"
)

func TestEpisodeContentTypeRegistry(t *testing.T) {
	c := &Client{}
	if got := c.episodeContentType("ep1"); got != model.ContentUnknown {
		t.Fatalf("unknown episode = %v, want ContentUnknown", got)
	}
	c.noteEpisodeContentTypes([]model.Episode{
		{ID: "ep1", ContentType: model.ContentPUGC},
		{ID: "ep2", ContentType: model.ContentUnknown}, // 0 不登记
		{ID: "", ContentType: model.ContentPUGC},       // 无 id 不登记
	})
	if got := c.episodeContentType("ep1"); got != model.ContentPUGC {
		t.Fatalf("ep1 = %v, want ContentPUGC", got)
	}
	if got := c.episodeContentType("ep2"); got != model.ContentUnknown {
		t.Fatalf("ep2 = %v, want ContentUnknown (zero must not register)", got)
	}
	c.noteEpisodeContentType("ep2", model.ContentMotionComic)
	if got := c.episodeContentType("ep2"); got != model.ContentMotionComic {
		t.Fatalf("ep2 after single note = %v, want ContentMotionComic", got)
	}
}

func TestIsContentGoneCode(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errors.New("playback code 101000"), true},
		{errors.New("playback code 101137"), true},
		{errors.New("playback code 101200"), false}, // 内容域上界外
		{errors.New("playback code 100001"), false},
		{errors.New("playback code abc"), false},
		{errors.New("playback model missing for requested episode"), false},
		{nil, false},
	} {
		if got := isContentGoneCode(tc.err); got != tc.want {
			t.Fatalf("isContentGoneCode(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
