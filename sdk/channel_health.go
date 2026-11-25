package sdk

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/beilunyang/openhongguo/sdk/model"
)

// 本地取址通道的健康跟踪。平台逐通道开关播放发放，且官方客户端的
// 发放通道在 POST 与 GET 之间换过（2026-11-16 起 v5 POST 发放、旧
// 通道全空；2026-11-20 又出现 v5 全天空返而 tab 多集 GET 在发放的
// 时段）。取址链若每次都从链首逐个试，死通道的空请求与随之而来的
// 身份丢弃/重注册会让每次取址都白付一遍。这里按通道记最近成败：
// 空发放的通道标 down 一段时间，链首选最近成功的通道、跳过 down
// 的通道；全部 down 时不跳过（相位随时翻转，必须整链重探）。

// channelDownDuration 是一条通道空发放后被跳过的时长。取得短：相位
// 翻转以分钟计，45 秒足够让排队中的取址先用活通道，过期后自动重探。
const channelDownDuration = 45 * time.Second

// 本地取址通道名，与取址链中的尝试一一对应；channelV5Batch 是批量
// 解析的 v5 POST，与单集 v5 同端点但独立记账（批量退化与单集链的
// 健康互不牵连）。
const (
	channelV5       = "v5"
	channelPlayerV1 = "playerv1"
	channelTabMulti = "tabmulti"
	channelPlayer   = "player"
	channelMultiV2  = "multiv2"
	channelMulti    = "multi"
	channelTab      = "tab"
	channelV5Batch  = "v5batch"
)

// resolveChannelOrder 是单集取址链里通道的经典顺序（健康计划的基准
// 序与全 down 时的回退序）。
var resolveChannelOrder = []string{
	channelV5, channelPlayerV1, channelTabMulti, channelPlayer, channelMultiV2, channelMulti, channelTab,
}

type channelState struct {
	lastSuccess time.Time
	downUntil   time.Time
	consecEmpty int
}

// channelHealth 是 Client 上的通道健康表。零值可直接用；全部状态由
// mu 保护，下载并发取址可安全共用。
type channelHealth struct {
	mu        sync.Mutex
	states    map[string]*channelState
	preferred string
}

func (h *channelHealth) stateLocked(name string) *channelState {
	if h.states == nil {
		h.states = make(map[string]*channelState)
	}
	st := h.states[name]
	if st == nil {
		st = &channelState{}
		h.states[name] = st
	}
	return st
}

// recordSuccess 记一次通道发放成功（拿到模型，随 ErrDRMProtected
// 返回的也算）：清掉 down 状态，并把该通道设为首选。
func (h *channelHealth) recordSuccess(name string, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.stateLocked(name)
	st.lastSuccess = now
	st.downUntil = time.Time{}
	st.consecEmpty = 0
	h.preferred = name
}

// recordEmpty 记一次空发放：通道标 down channelDownDuration。
func (h *channelHealth) recordEmpty(name string, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.stateLocked(name)
	st.consecEmpty++
	st.downUntil = now.Add(channelDownDuration)
}

// isDown 报告通道当前是否处于 down 窗口内。
func (h *channelHealth) isDown(name string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.states[name]
	return st != nil && now.Before(st.downUntil)
}

// chainPlan 是取址链开始时按健康状态定下的本轮尝试计划（快照）：
// 本轮内新记的 down 不改变本轮计划——链中的新鲜身份重试等结构靠
// 完整走完一轮来恢复身份，不能被中途的标记截断。
type chainPlan struct {
	down      map[string]bool
	allDown   bool
	preferred string
}

// plan 按当前健康状态生成一轮取址计划：首选是最近成功且未 down 的
// 通道；全部通道都 down 时 allDown 为真，计划不跳过任何通道（按
// 经典顺序整链重探，相位翻转靠这轮重新发现）。
func (h *channelHealth) plan(now time.Time) chainPlan {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := chainPlan{down: make(map[string]bool, len(resolveChannelOrder))}
	downCount := 0
	for _, name := range resolveChannelOrder {
		if st := h.states[name]; st != nil && now.Before(st.downUntil) {
			p.down[name] = true
			downCount++
		}
	}
	p.allDown = downCount == len(resolveChannelOrder)
	if pref := h.preferred; pref != "" {
		known := false
		for _, name := range resolveChannelOrder {
			if name == pref {
				known = true
				break
			}
		}
		if known && !p.down[pref] {
			p.preferred = pref
		}
	}
	return p
}

// attempt 报告本轮计划是否应尝试该通道的常规位置。全部 down 时恒
// 为真（整链重探）。
func (p chainPlan) attempt(name string) bool {
	return p.allDown || !p.down[name]
}

// noteChannelOutcome 把一次通道尝试的结果记进健康跟踪：拿到模型是
// 成功；空发放标 down。两类结果不归咎于通道、不记录：上下文取消
// （调用方自己放弃，通道无从判断），与设备被拒 ErrDeviceRejected
// （身份被服务端拒绝，是身份问题不是通道问题）。注意
// ErrAnonymousLimited 不在此列：它就是「服务端返回空结果」的信号
// 本身——200 空响应体在 readAnonymous 里正映射为它——属于空发放。
func (c *Client) noteChannelOutcome(ctx context.Context, name string, playback *model.Playback, err error) {
	if playback != nil {
		c.channels.recordSuccess(name, c.clock())
		return
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if errors.Is(err, ErrDeviceRejected) {
		return
	}
	c.channels.recordEmpty(name, c.clock())
}
