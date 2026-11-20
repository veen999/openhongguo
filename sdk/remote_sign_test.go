package sdk

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type remoteStubStore struct{ p *DeviceProfile }

func (s *remoteStubStore) Load(context.Context) (*DeviceProfile, error) {
	if s.p == nil {
		return nil, ErrProfileNotFound
	}
	return s.p, nil
}

func (s *remoteStubStore) Save(_ context.Context, p *DeviceProfile) error { s.p = p; return nil }
func (s *remoteStubStore) Reset(context.Context) error                     { s.p = nil; return nil }

type remoteStubTransport struct {
	handler func(req *Request) (*Response, error)
}

func (s remoteStubTransport) Do(_ context.Context, req *Request) (*Response, error) {
	return s.handler(req)
}

func remoteTestClient(t *testing.T, handler func(req *Request) (*Response, error)) *Client {
	t.Helper()
	c, err := NewClient(Options{Transport: remoteStubTransport{handler}, ProfileStore: &remoteStubStore{}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func jsonResponse(status int, v any) *Response {
	b, _ := json.Marshal(v)
	return &Response{StatusCode: status, Headers: map[string]string{}, Body: b}
}

const sixHeadersJSON = `{"X-Gorgon":"g","X-Khronos":"123","X-Argus":"a","X-Ladon":"l","X-Medusa":"m","X-Helios":"h"}`

func TestUnidbgSignReturnsAllHeaders(t *testing.T) {
	var gotURL string
	c := remoteTestClient(t, func(req *Request) (*Response, error) {
		if !strings.Contains(req.URL, "feature-hash") {
			t.Errorf("unexpected url %s", req.URL)
		}
		var payload map[string]string
		if err := json.Unmarshal(req.Body, &payload); err != nil {
			t.Fatal(err)
		}
		gotURL = payload["url"]
		return &Response{StatusCode: 200, Headers: map[string]string{}, Body: []byte(`{"headers":` + sixHeadersJSON + `}`)}, nil
	})
	sigs, err := c.unidbgSign(context.Background(), "https://example.com/tab?video_id=1")
	if err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://example.com/tab?video_id=1" {
		t.Errorf("payload url = %q", gotURL)
	}
	if len(sigs) != 6 || sigs["x-medusa"] != "m" || sigs["x-gorgon"] != "g" {
		t.Errorf("sigs = %v", sigs)
	}
}

func TestUnidbgSignRejectsIncompleteHeaders(t *testing.T) {
	c := remoteTestClient(t, func(req *Request) (*Response, error) {
		return &Response{StatusCode: 200, Headers: map[string]string{}, Body: []byte(`{"headers":{"X-Argus":"a"}}`)}, nil
	})
	if _, err := c.unidbgSign(context.Background(), "https://example.com/x"); err == nil ||
		!strings.Contains(err.Error(), "未返回完整签名") {
		t.Errorf("err = %v", err)
	}
}

func TestUnidbgSignSurfacesServiceError(t *testing.T) {
	c := remoteTestClient(t, func(req *Request) (*Response, error) {
		return jsonResponse(500, map[string]string{"error": "signer busy"}), nil
	})
	if _, err := c.unidbgSign(context.Background(), "https://example.com/x"); err == nil ||
		!strings.Contains(err.Error(), "signer busy") {
		t.Errorf("err = %v", err)
	}
}

func TestFridaProfileAndSign(t *testing.T) {
	c := remoteTestClient(t, func(req *Request) (*Response, error) {
		switch {
		case strings.Contains(req.URL, "/info"):
			return &Response{StatusCode: 200, Headers: map[string]string{}, Body: []byte(`{
				"supportsBodySigning": true,
				"deviceProfile": {
					"user_agent": "ua-from-service",
					"params": {"device_id":"111","iid":"222","cdid":"333","aid":"8662","extra":"x"}
				}}`)}, nil
		case strings.Contains(req.URL, "/sign"):
			return &Response{StatusCode: 200, Headers: map[string]string{}, Body: []byte(sixHeadersJSON)}, nil
		}
		t.Errorf("unexpected url %s", req.URL)
		return jsonResponse(404, nil), nil
	})
	prof, err := c.fridaProfileCached(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if prof.userAgent != "ua-from-service" || prof.params["device_id"] != "111" || !prof.supportsBodySigning {
		t.Errorf("profile = %+v", prof)
	}
	// 第二次走缓存：handler 若再收到 /info 会因 switch 命中而返回档案，
	// 这里只断言不报错且档案不变。
	prof2, err := c.fridaProfileCached(context.Background())
	if err != nil || prof2 != prof {
		t.Errorf("cached profile = %v, %v", prof2, err)
	}
	sigs, err := c.fridaSign(context.Background(), "https://example.com/tab?video_id=9")
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 6 || sigs["x-helios"] != "h" {
		t.Errorf("sigs = %v", sigs)
	}
}

func TestFridaProfileRejectsIncomplete(t *testing.T) {
	c := remoteTestClient(t, func(req *Request) (*Response, error) {
		return &Response{StatusCode: 200, Headers: map[string]string{}, Body: []byte(`{"deviceProfile":{"params":{"device_id":"1"},"user_agent":"ua"}}`)}, nil
	})
	if _, err := c.fridaProfileCached(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "尚未就绪") {
		t.Errorf("err = %v", err)
	}
}

func TestSetSigningModeParsing(t *testing.T) {
	c := remoteTestClient(t, func(req *Request) (*Response, error) { return jsonResponse(200, nil), nil })
	c.SetSigningMode("unidbg")
	if c.signingMode() != SigningModeUnidbg {
		t.Error("unidbg not stored")
	}
	c.SetSigningMode(" FRIDA ")
	if c.signingMode() != SigningModeFrida {
		t.Error("frida not stored")
	}
	c.SetSigningMode("nonsense")
	if c.signingMode() != SigningModeLocal {
		t.Error("unknown mode must fall back to local")
	}
}

func TestEncodeRemoteQueryMatchesLocalShape(t *testing.T) {
	got := encodeRemoteQuery([][2]string{
		{"resolution", "1200*2670"},
		{"q", "a b"},
		{"wave", "~x"},
	})
	want := "resolution=1200*2670&q=a%20b&wave=~x"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
