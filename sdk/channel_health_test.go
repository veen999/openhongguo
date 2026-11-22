package sdk

// 通道健康跟踪的单元测试：标 down 与跳过、down 过期、首选晋升、
// 全部 down 时整链重探的回退，以及尝试结果的归类（noteChannelOutcome）。

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/beilunyang/openhongguo/sdk/model"
)

var healthT0 = time.Unix(1_700_000_000, 0)

func TestChannelHealthMarkDownAndSkip(t *testing.T) {
	h := &channelHealth{}
	if p := h.plan(healthT0); !p.attempt(channelV5) || p.allDown || p.preferred != "" {
		t.Fatalf("fresh plan = %+v, want all attemptable, no preferred", p)
	}
	h.recordEmpty(channelV5, healthT0)
	if !h.isDown(channelV5, healthT0) {
		t.Fatal("v5 should be down right after an empty result")
	}
	p := h.plan(healthT0)
	if p.attempt(channelV5) {
		t.Fatal("plan should skip the downed v5 channel")
	}
	if !p.attempt(channelTabMulti) || !p.attempt(channelPlayer) {
		t.Fatal("plan should still attempt healthy channels")
	}
	if p.allDown {
		t.Fatal("allDown must be false while any channel is healthy")
	}
	// 连续空发放计数累加。
	h.recordEmpty(channelV5, healthT0)
	if got := h.states[channelV5].consecEmpty; got != 2 {
		t.Fatalf("consecEmpty = %d, want 2", got)
	}
}

func TestChannelHealthDownExpires(t *testing.T) {
	h := &channelHealth{}
	h.recordEmpty(channelV5, healthT0)
	if !h.isDown(channelV5, healthT0.Add(channelDownDuration-time.Second)) {
		t.Fatal("v5 should still be down inside the window")
	}
	if h.isDown(channelV5, healthT0.Add(channelDownDuration+time.Second)) {
		t.Fatal("v5 should recover after the down window")
	}
	p := h.plan(healthT0.Add(channelDownDuration + time.Second))
	if !p.attempt(channelV5) {
		t.Fatal("plan should attempt v5 again after the window")
	}
}

func TestChannelHealthSuccessClearsDownAndPromotes(t *testing.T) {
	h := &channelHealth{}
	h.recordEmpty(channelTabMulti, healthT0)
	h.recordSuccess(channelTabMulti, healthT0.Add(time.Second))
	if h.isDown(channelTabMulti, healthT0.Add(time.Second)) {
		t.Fatal("success must clear the down state")
	}
	if got := h.states[channelTabMulti].consecEmpty; got != 0 {
		t.Fatalf("consecEmpty = %d, want 0 after success", got)
	}
	p := h.plan(healthT0.Add(time.Second))
	if p.preferred != channelTabMulti {
		t.Fatalf("preferred = %q, want %q", p.preferred, channelTabMulti)
	}
	if !p.attempt(channelTabMulti) {
		t.Fatal("preferred channel must be attemptable")
	}
}

func TestChannelHealthPreferredMovesAndDownPreferredHidden(t *testing.T) {
	h := &channelHealth{}
	h.recordSuccess(channelTabMulti, healthT0)
	h.recordSuccess(channelV5, healthT0.Add(time.Second))
	if p := h.plan(healthT0.Add(time.Second)); p.preferred != channelV5 {
		t.Fatalf("preferred = %q, want latest success %q", p.preferred, channelV5)
	}
	// 首选通道空发放后：计划不再把它当首选，且跳过它。
	h.recordEmpty(channelV5, healthT0.Add(2*time.Second))
	p := h.plan(healthT0.Add(2 * time.Second))
	if p.preferred != "" {
		t.Fatalf("preferred = %q, want none while the preferred channel is down", p.preferred)
	}
	if p.attempt(channelV5) {
		t.Fatal("downed ex-preferred channel should be skipped")
	}
	if !p.attempt(channelTabMulti) {
		t.Fatal("other healthy channels stay attemptable")
	}
}

func TestChannelHealthAllDownFallsBackToFullChain(t *testing.T) {
	h := &channelHealth{}
	for _, name := range resolveChannelOrder {
		h.recordEmpty(name, healthT0)
	}
	p := h.plan(healthT0)
	if !p.allDown {
		t.Fatal("allDown should be true when every channel is down")
	}
	for _, name := range resolveChannelOrder {
		if !p.attempt(name) {
			t.Fatalf("all-down plan must still attempt %s (phases flip; re-probe everything)", name)
		}
	}
	if p.preferred != "" {
		t.Fatalf("preferred = %q, want none when all channels are down", p.preferred)
	}
	// 任一通道在窗口外恢复，计划回到跳过模式。
	p = h.plan(healthT0.Add(channelDownDuration + time.Second))
	if p.allDown {
		t.Fatal("allDown should clear once the down windows expire")
	}
}

func TestChannelHealthBatchChannelIsSeparate(t *testing.T) {
	h := &channelHealth{}
	h.recordEmpty(channelV5Batch, healthT0)
	if !h.isDown(channelV5Batch, healthT0) {
		t.Fatal("v5batch should be down after an empty batch result")
	}
	// 批量通道的 down 不影响单集链计划。
	p := h.plan(healthT0)
	if !p.attempt(channelV5) || p.allDown {
		t.Fatalf("single-chain plan affected by batch health: %+v", p)
	}
	// 批量成功也不得占据单集链的首选位。
	h.recordSuccess(channelV5Batch, healthT0)
	if h.isDown(channelV5Batch, healthT0) {
		t.Fatal("batch success must clear the batch down state")
	}
	if p := h.plan(healthT0); p.preferred != "" {
		t.Fatalf("preferred = %q, want none (batch is not a chain channel)", p.preferred)
	}
}

func TestChannelHealthConcurrentUse(t *testing.T) {
	h := &channelHealth{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := resolveChannelOrder[i%len(resolveChannelOrder)]
			for j := 0; j < 50; j++ {
				h.recordEmpty(name, healthT0)
				h.recordSuccess(name, healthT0)
				_ = h.plan(healthT0)
				_ = h.isDown(name, healthT0)
			}
		}(i)
	}
	wg.Wait()
}

func healthTestClient() *Client {
	return &Client{clock: func() time.Time { return healthT0 }}
}

func TestNoteChannelOutcomeClassification(t *testing.T) {
	ctx := context.Background()

	// 拿到模型（含受保护模型随错误返回）记成功并晋升首选。
	c := healthTestClient()
	c.noteChannelOutcome(ctx, channelTabMulti, &model.Playback{}, ErrDRMProtected)
	if p := c.channels.plan(healthT0); p.preferred != channelTabMulti {
		t.Fatalf("preferred = %q, want tabmulti after a model-bearing result", p.preferred)
	}

	// 空发放记 down：ErrAnonymousLimited（200 空响应体的映射）与
	// 一般解析错误都算。
	for _, err := range []error{ErrAnonymousLimited, errors.New("no entry for episode")} {
		c := healthTestClient()
		c.noteChannelOutcome(ctx, channelV5, nil, err)
		if !c.channels.isDown(channelV5, healthT0) {
			t.Fatalf("err %v should mark v5 down", err)
		}
	}

	// 不归咎于通道：上下文取消、设备被拒（含包装）。
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{"ctx cancelled", cancelledCtx, errors.New("connection reset")},
		{"context.Canceled", ctx, context.Canceled},
		{"context.DeadlineExceeded", ctx, context.DeadlineExceeded},
		{"device rejected", ctx, ErrDeviceRejected},
		{"wrapped device rejected", ctx, fmt.Errorf("read: %w", ErrDeviceRejected)},
	} {
		c := healthTestClient()
		c.noteChannelOutcome(tc.ctx, channelV5, nil, tc.err)
		if c.channels.isDown(channelV5, healthT0) {
			t.Fatalf("%s: v5 must not be marked down", tc.name)
		}
		if p := c.channels.plan(healthT0); !p.attempt(channelV5) {
			t.Fatalf("%s: plan should still attempt v5", tc.name)
		}
	}
}
