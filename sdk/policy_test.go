package sdk

// 算法策略层测试（镜像 hongguoTV PlaybackAlgorithmPolicy 的语义）：
// 本地全链取址失败后先多通道复核本地通道，全部确认不可用才自动切
// 到 unidbg 并重试本次（2026-11-23 规则）；远程成功后后台多通道复
// 探本地，任一通道恢复则切回本地；重复下发同一用户选择不重置策略。

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
	// recoverAt（UnixNano）非 0 时，本地 v5 在该时刻后开始发模型，
	// 模拟试用期结束、通道随身份存活恢复。
	recoverAt atomic.Int64
}

// disableProbation 关掉试用期复探（默认目标年龄设为负值 = 身份永
// 远已达标，构造客户端时定版），供不测验试用期行为的用例隔离；
// 测验试用期的用例用 Options.ProbationWindow 显式调小。
func disableProbation(t *testing.T) {
	t.Helper()
	oldT, oldI := probationTargetAge, probationReprobeInterval
	probationTargetAge = -1
	t.Cleanup(func() { probationTargetAge, probationReprobeInterval = oldT, oldI })
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
	case strings.Contains(url, "multi_video_model/v1"):
		// 服务重签后的 v5 请求带占位 medusa（"m"）：对其发模型，模拟
		// 本地签名被平台拒发、远程签名可发的情形；serveV5 置位则模拟
		// 本地通道恢复，本地签名的请求也发模型。
		medusa := ""
		for k, v := range req.Headers {
			if strings.EqualFold(k, "x-medusa") {
				medusa = v
			}
		}
		if f.serveV5.Load() || medusa == "m" || (f.recoverAt.Load() != 0 && time.Now().UnixNano() > f.recoverAt.Load()) {
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

func TestPolicyAutoSwitchOnFirstLocalFailure(t *testing.T) {
	disableProbation(t)
	fake := &policyFake{}
	c := policyTestClient(t, fake)
	c.SetSigningMode("local")
	ctx := context.Background()

	// 案二（一次直达）：本地第一次失败即在同一次取址内切换并重试，
	// 调用方直接拿到远程结果，不感知失败。
	pb, err := c.ResolvePlayback(ctx, policyEpisodeID, PlaybackOptions{})
	if pb == nil {
		t.Fatalf("first resolve should auto-switch to unidbg in-call and succeed, err = %v", err)
	}
	if got := c.EffectiveSigningMode(); got != "unidbg" {
		t.Fatalf("after first failure effective = %q, want unidbg", got)
	}
}

func TestPolicyProbeSwitchesBackToLocal(t *testing.T) {
	disableProbation(t)
	fake := &policyFake{}
	c := policyTestClient(t, fake)
	c.SetSigningMode("local")
	ctx := context.Background()
	// 先触发自动切换到 unidbg（同上，一次直达）。
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

func TestSetSigningModeAutoKeepsAnchorAndProbe(t *testing.T) {
	disableProbation(t)
	fake := &policyFake{}
	c := policyTestClient(t, fake)
	c.SetSigningMode("local")
	ctx := context.Background()

	// App 同步回写：生效方式镜像为 unidbg，但用户锚点仍是本地。
	c.SetSigningModeAuto("unidbg")
	if got := c.EffectiveSigningMode(); got != "unidbg" {
		t.Fatalf("effective = %q, want unidbg after auto sync", got)
	}
	if got := c.signingMode(); got != SigningModeLocal {
		t.Fatalf("anchor changed by auto sync: %d", got)
	}
	// 锚点仍是本地 → 远程成功后仍会探活，本地恢复即切回。
	fake.serveV5.Store(true)
	if pb, _ := c.ResolvePlayback(ctx, policyEpisodeID, PlaybackOptions{}); pb == nil {
		t.Fatal("remote resolve should succeed")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c.EffectiveSigningMode() == "local" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := c.EffectiveSigningMode(); got != "local" {
		t.Fatalf("effective = %q, want local after probe success", got)
	}
	// 用户手动改选后，自动同步入口必须失效（不得覆盖用户选择）。
	c.SetSigningMode("unidbg")
	c.SetSigningModeAuto("local")
	if got := c.EffectiveSigningMode(); got != "unidbg" {
		t.Fatalf("auto sync overrode manual selection: effective = %q", got)
	}
}

func TestPolicyProbationReprobeRecovers(t *testing.T) {
	// 试用期场景：本地通道在身份养到一定年龄后恢复。复核不得在第
	// 一轮全灭时就切远程，必须在同一身份上间隔复探、等到恢复，并
	// 以本地方式完成本次取址。
	oldT, oldI := probationTargetAge, probationReprobeInterval
	probationTargetAge = 4 * time.Second
	probationReprobeInterval = 1 * time.Second
	t.Cleanup(func() { probationTargetAge, probationReprobeInterval = oldT, oldI })

	fake := &policyFake{}
	fake.recoverAt.Store(time.Now().Add(2500 * time.Millisecond).UnixNano())
	prof, err := NewAnonymousDeviceProfile(nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	prof.DeviceID = "111222"
	prof.InstallID = "333444"
	c, err := NewClient(Options{
		Transport:       remoteStubTransport{handler: fake.handler},
		ProfileStore:    &remoteStubStore{p: prof},
		ProbationWindow: 4 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	c.SetSigningMode("local")
	pb, err := c.ResolvePlayback(context.Background(), policyEpisodeID, PlaybackOptions{})
	if pb == nil {
		t.Fatalf("probation resolve should recover locally, err = %v", err)
	}
	if got := c.EffectiveSigningMode(); got != "local" {
		t.Fatalf("effective = %q, want local (probation recovery must not flip)", got)
	}
}

func TestStartupProbePreFlipsWhenConfirmedDown(t *testing.T) {
	disableProbation(t)
	fake := &policyFake{}
	c := policyTestClient(t, fake)
	c.SetSigningMode("local")
	// 启动式齐射探测全灭 → 后台完整复核确认全灭 → 生效方式预切到
	// unidbg（锚点仍是本地）。
	results := c.ProbePlaybackChannels(context.Background())
	for name, alive := range results {
		if alive {
			t.Fatalf("setup: channel %s unexpectedly alive", name)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.EffectiveSigningMode() == "unidbg" {
			if got := c.signingMode(); got != SigningModeLocal {
				t.Fatalf("anchor changed by pre-flip: %d", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("effective = %q, want unidbg after confirmed-down startup probe", c.EffectiveSigningMode())
}
