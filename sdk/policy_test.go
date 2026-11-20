package sdk

// 算法策略层测试（镜像 hongguoTV PlaybackAlgorithmPolicy 的语义）：
// 本地连续两次取址失败自动切到 unidbg 并重试本次；远程成功后后台
// 探测本地 v5 通道，恢复则切回本地；重复下发同一用户选择不重置策略。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const policyEpisodeID = "7100000000000201"

const tabModelJSON = `{"code":0,"data":{"video_model":{"status":10,"video_id":"7100000000000201","video_list":{"540p":{"main_url":"https://media.invalid/540.mp4","video_meta":{"definition":"540p","vheight":960}}}}}}`

type policyFake struct {
	serveV5 atomic.Bool
}

func (f *policyFake) handler(req *Request) (*Response, error) {
	url := req.URL
	switch {
	case strings.Contains(url, "device_register"):
		return &Response{StatusCode: 200, Headers: map[string]string{},
			Body: []byte(`{"device_id_str":"333444","install_id_str":"555666","device_token":"tok"}`)}, nil
	case strings.Contains(url, "feature-hash"):
		return &Response{StatusCode: 200, Headers: map[string]string{},
			Body: []byte(`{"headers":` + sixHeadersJSON + `}`)}, nil
	case strings.Contains(url, "multi_video_model/player/v1"):
		// 服务重签后的 v5 请求带占位 medusa（"m"）：对其发模型，模拟
		// 本地签名被平台拒发、远程签名可发的情形；serveV5 置位则模拟
		// 本地通道恢复，本地签名的请求也发模型。
		medusa := ""
		for k, v := range req.Headers {
			if strings.EqualFold(k, "x-medusa") {
				medusa = v
			}
		}
		if f.serveV5.Load() || medusa == "m" {
			raw, err := os.ReadFile(filepath.Join("..", "testdata", "protocol", "drm-playback-v5.json"))
			if err != nil {
				return nil, err
			}
			return &Response{StatusCode: 200, Headers: map[string]string{}, Body: raw}, nil
		}
		return &Response{StatusCode: 200, Headers: map[string]string{}}, nil
	case strings.Contains(url, "video_tab/video_model"):
		return &Response{StatusCode: 200, Headers: map[string]string{}}, nil
	}
	return &Response{StatusCode: 200, Headers: map[string]string{}}, nil
}

func policyTestClient(t *testing.T, fake *policyFake) *Client {
	t.Helper()
	prof, err := NewAnonymousDeviceProfile(nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	prof.DeviceID = "111222"
	prof.InstallID = "333444"
	c, err := NewClient(Options{
		Transport:    remoteStubTransport{handler: fake.handler},
		ProfileStore: &remoteStubStore{p: prof},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPolicyAutoSwitchAfterTwoLocalFailures(t *testing.T) {
	fake := &policyFake{}
	c := policyTestClient(t, fake)
	c.SetSigningMode("local")
	ctx := context.Background()

	if pb, _ := c.ResolvePlayback(ctx, policyEpisodeID, PlaybackOptions{}); pb != nil {
		t.Fatalf("first resolve should fail locally, got %+v", pb)
	}
	if got := c.EffectiveSigningMode(); got != "local" {
		t.Fatalf("after 1 failure effective = %q, want local", got)
	}
	pb, err := c.ResolvePlayback(ctx, policyEpisodeID, PlaybackOptions{})
	if pb == nil {
		t.Fatalf("second resolve should auto-switch to unidbg and succeed, err = %v", err)
	}
	if got := c.EffectiveSigningMode(); got != "unidbg" {
		t.Fatalf("after 2 failures effective = %q, want unidbg", got)
	}
}

func TestPolicyProbeSwitchesBackToLocal(t *testing.T) {
	fake := &policyFake{}
	c := policyTestClient(t, fake)
	c.SetSigningMode("local")
	ctx := context.Background()
	// 先触发自动切换到 unidbg（同上）。
	_, _ = c.ResolvePlayback(ctx, policyEpisodeID, PlaybackOptions{})
	if pb, _ := c.ResolvePlayback(ctx, policyEpisodeID, PlaybackOptions{}); pb == nil {
		t.Fatal("setup: expected auto-switch resolve to succeed via unidbg")
	}
	if got := c.EffectiveSigningMode(); got != "unidbg" {
		t.Fatalf("setup: effective = %q, want unidbg", got)
	}
	// 本地 v5 通道恢复：远程再成功一次，后台探测应把生效方式切回本地。
	fake.serveV5.Store(true)
	if pb, _ := c.ResolvePlayback(ctx, policyEpisodeID, PlaybackOptions{}); pb == nil {
		t.Fatal("remote resolve should still succeed")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c.EffectiveSigningMode() == "local" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("effective = %q, want local after probe success", c.EffectiveSigningMode())
}

func TestSetSigningModeSameValueKeepsPolicyState(t *testing.T) {
	fake := &policyFake{}
	c := policyTestClient(t, fake)
	c.SetSigningMode("unidbg")
	// 模拟策略层已把生效方式切回本地；重复下发同一用户选择不得重置。
	c.effSignMode.Store(SigningModeLocal)
	c.localFailCount.Store(1)
	c.SetSigningMode("unidbg")
	if got := c.EffectiveSigningMode(); got != "local" {
		t.Fatalf("same-value SetSigningMode reset effective mode to %q", got)
	}
	if got := c.localFailCount.Load(); got != 1 {
		t.Fatalf("same-value SetSigningMode reset failure count to %d", got)
	}
	// 换一个不同的选择则应立即生效并清零计数。
	c.SetSigningMode("frida")
	if got := c.EffectiveSigningMode(); got != "frida" {
		t.Fatalf("effective = %q, want frida", got)
	}
	if got := c.localFailCount.Load(); got != 0 {
		t.Fatalf("failure count = %d, want 0", got)
	}
}
