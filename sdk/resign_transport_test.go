package sdk

// 传输层重签测试：unidbg 生效时，播放取址请求（带 x-medusa）在发出前
// 被交给签名服务换掉六个签名头；本地方式、探活绕过、非播放请求都
// 不触碰；签名服务失败时请求不发出、错误向上返回。

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

type resignRecorder struct {
	signCalls     int
	platformCalls int
	lastPayload   map[string]string
	lastMedusa    string
	signStatus    int
	signBody      string
}

func (r *resignRecorder) handler(req *Request) (*Response, error) {
	if strings.Contains(req.URL, "feature-hash") {
		r.signCalls++
		_ = json.Unmarshal(req.Body, &r.lastPayload)
		status := r.signStatus
		if status == 0 {
			status = 200
		}
		body := r.signBody
		if body == "" {
			body = `{"headers":` + sixHeadersJSON + `}`
		}
		return &Response{StatusCode: status, Headers: map[string]string{}, Body: []byte(body)}, nil
	}
	r.platformCalls++
	for k, v := range req.Headers {
		if strings.EqualFold(k, "x-medusa") {
			r.lastMedusa = v
		}
	}
	return &Response{StatusCode: 200, Headers: map[string]string{}, Body: []byte(`{}`)}, nil
}

func resignTestClient(t *testing.T, rec *resignRecorder, mode string) *Client {
	t.Helper()
	c := remoteTestClient(t, rec.handler)
	c.SetSigningMode(mode)
	return c
}

func v5Request() *Request {
	return &Request{
		Method:  "POST",
		URL:     "https://api5-normal-sinfonlinea.fqnovel.com/novel/player/multi_video_model/player/v1?device_id=1",
		Headers: map[string]string{"x-medusa": "local-long-medusa", "x-khronos": "999", "x-ss-stub": "ABC"},
		Body:    []byte{0x1f, 0x8b, 0x08, 0x00},
	}
}

func TestResignReplacesSignatureHeadersForPlayback(t *testing.T) {
	rec := &resignRecorder{}
	c := resignTestClient(t, rec, "unidbg")
	req := v5Request()
	if _, err := c.transport.Do(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if rec.signCalls != 1 || rec.platformCalls != 1 {
		t.Fatalf("sign=%d platform=%d, want 1/1", rec.signCalls, rec.platformCalls)
	}
	if rec.lastPayload["url"] != req.URL {
		t.Errorf("signer url = %q", rec.lastPayload["url"])
	}
	if rec.lastPayload["body_hex"] != hex.EncodeToString(req.Body) {
		t.Errorf("signer body_hex = %q", rec.lastPayload["body_hex"])
	}
	if rec.lastMedusa != "m" {
		t.Errorf("platform medusa = %q, want service value m", rec.lastMedusa)
	}
	if req.Headers["x-ss-stub"] != "ABC" {
		t.Errorf("non-signature header lost: %v", req.Headers)
	}
}

func TestResignInactiveInLocalMode(t *testing.T) {
	rec := &resignRecorder{}
	c := resignTestClient(t, rec, "local")
	req := v5Request()
	if _, err := c.transport.Do(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if rec.signCalls != 0 || rec.lastMedusa != "local-long-medusa" {
		t.Errorf("sign=%d medusa=%q, want untouched", rec.signCalls, rec.lastMedusa)
	}
}

func TestResignBypassedForLocalProbe(t *testing.T) {
	rec := &resignRecorder{}
	c := resignTestClient(t, rec, "unidbg")
	req := v5Request()
	if _, err := c.transport.Do(withResignBypass(context.Background()), req); err != nil {
		t.Fatal(err)
	}
	if rec.signCalls != 0 || rec.lastMedusa != "local-long-medusa" {
		t.Errorf("probe request was re-signed: sign=%d medusa=%q", rec.signCalls, rec.lastMedusa)
	}
}

func TestResignSkipsNonPlaybackRequests(t *testing.T) {
	rec := &resignRecorder{}
	c := resignTestClient(t, rec, "unidbg")
	req := &Request{
		Method:  "GET",
		URL:     "https://api5-normal-sinfonlinea.fqnovel.com/reading/bookapi/search/tab/v1?query=x",
		Headers: map[string]string{"x-medusa": "local-long-medusa"},
	}
	if _, err := c.transport.Do(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if rec.signCalls != 0 {
		t.Errorf("catalog request was re-signed")
	}
}

func TestResignSignerFailureBlocksRequest(t *testing.T) {
	rec := &resignRecorder{signStatus: 500, signBody: `{"error":"signer busy"}`}
	c := resignTestClient(t, rec, "unidbg")
	_, err := c.transport.Do(context.Background(), v5Request())
	if err == nil || !strings.Contains(err.Error(), "signer busy") {
		t.Fatalf("err = %v, want signer busy surfaced", err)
	}
	if rec.platformCalls != 0 {
		t.Errorf("request was sent despite signer failure")
	}
}

func TestResignRejectsIncompleteHeaders(t *testing.T) {
	rec := &resignRecorder{signBody: `{"headers":{"X-Argus":"a"}}`}
	c := resignTestClient(t, rec, "unidbg")
	_, err := c.transport.Do(context.Background(), v5Request())
	if err == nil || !strings.Contains(err.Error(), "未返回完整签名") {
		t.Fatalf("err = %v", err)
	}
	if rec.platformCalls != 0 {
		t.Errorf("request was sent despite incomplete signature")
	}
}
