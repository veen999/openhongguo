package sdk

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/beilunyang/openhongguo/internal/protocol"
)

func vm5Fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "protocol", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Regression: the sparse video_model/v1 channel heads the local chain.
// Like every other channel it must surface a protected model as
// (playback, ErrDRMProtected) — the bindings attach the derived content
// key only on that signal. Returning (playback, nil) left the app with a
// protected model and no key ("加密集缺少解密密钥").
func TestVideoModelV5ProtectedSurfacesKey(t *testing.T) {
	reg := vm5Fixture(t, "register-response.json")
	model := vm5Fixture(t, "videomodelv5-playback.json")
	c, err := NewClient(Options{
		Transport: remoteStubTransport{handler: func(req *Request) (*Response, error) {
			switch {
			case strings.Contains(req.URL, protocol.PathRegister):
				return &Response{StatusCode: 200, Headers: map[string]string{"Set-Cookie": "ttreq=fixture-ticket; Path=/"}, Body: reg}, nil
			case strings.Contains(req.URL, protocol.PathPlayback):
				return &Response{StatusCode: 200, Headers: map[string]string{}, Body: model}, nil
			}
			return &Response{StatusCode: 200, Headers: map[string]string{}, Body: []byte(`{}`)}, nil
		}},
		ProfileStore:    &remoteStubStore{},
		ProbationWindow: -1,
		Clock:           func() time.Time { return time.UnixMilli(1_700_000_000_123).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := c.ResolvePlayback(context.Background(), "7513853006417234969", PlaybackOptions{})
	if pb == nil {
		t.Fatalf("playback is nil, err = %v", err)
	}
	if !errors.Is(err, ErrDRMProtected) {
		t.Fatalf("err = %v, want ErrDRMProtected alongside the protected model", err)
	}
	if len(pb.Streams) != 5 {
		t.Fatalf("streams = %d, want 5", len(pb.Streams))
	}
	key, kerr := ContentKey(pb)
	if kerr != nil {
		t.Fatalf("ContentKey: %v", kerr)
	}
	if key.Hex == "" {
		t.Fatal("content key is empty")
	}
}
