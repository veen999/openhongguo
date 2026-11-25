package sdk

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/beilunyang/openhongguo/sdk/model"
)

// SigningModeProbe 是 ProbeSigningMode 的单次检测结果，供设置页
// 「算法模式」弹层展示：可用性、耗时、最高画质与失败类别。
type SigningModeProbe struct {
	Mode          string `json:"mode"`
	OK            bool   `json:"ok"`
	LatencyMs     int64  `json:"latencyMs"`
	MaxDefinition string `json:"maxDefinition,omitempty"` // 如 "1080P"；失败时为空
	ErrorKind     string `json:"errorKind,omitempty"`     // empty / timeout / unreachable / error
}

// ProbeSigningMode 快速检测一种计算方式「现在能不能取到流」：
// 弹层检测只做快速实测，不做深度确认——深度确认（多通道复核、
// 试用期复探）是后台判定缓存的活，不该让用户盯着弹层等（2026-11-25
// 链路级口径下本地检测跑满 128 秒且确认按钮被检测锁死，用户实测
// 痛斥后改为快速口径）。
//
//   - 本地：在当前播放身份上顺序单发 tab 多集与 v5 两条历史上会发
//     放的通道，任一出模型即可用，预算 15 秒封顶。测到可用记
//     「可用」判定；测不到不下「不可用」结论（可能只是试用期/通
//     道轮换），只触发后台完整复核去定判定。
//   - unidbg：临时把生效方式切到 unidbg 跑一遍取址链（传输层重
//     签），测完恢复，预算 45 秒。
//   - frida：走远程真机通道（tab GET 全灭后有服务签名的 POST 播
//     放路由兜底），预算 45 秒。
//
// 检测不改锚点、不触发策略层的自动切换。
func (c *Client) ProbeSigningMode(ctx context.Context, mode string) SigningModeProbe {
	res := SigningModeProbe{Mode: "local"}
	var modeID int32 = SigningModeLocal
	switch mode {
	case "unidbg":
		modeID, res.Mode = SigningModeUnidbg, "unidbg"
	case "frida":
		modeID, res.Mode = SigningModeFrida, "frida"
	}
	budget := 15 * time.Second
	switch modeID {
	case SigningModeUnidbg, SigningModeFrida:
		budget = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	start := c.clock()
	var pb *model.Playback
	var err error
	switch modeID {
	case SigningModeFrida:
		pb, err = c.resolvePlaybackViaRemoteMode(withResignBypass(ctx), probeEpisodeID, SigningModeFrida)
	case SigningModeUnidbg:
		old := c.effSignMode.Swap(SigningModeUnidbg)
		pb, err = c.resolvePlaybackChain(ctx, probeEpisodeID, SigningModeUnidbg)
		c.effSignMode.Store(old)
	default:
		bypassCtx := withResignBypass(ctx)
		var p *DeviceProfile
		p, err = c.playbackIdentity(bypassCtx)
		if err == nil {
			if m, _ := c.resolvePlaybackViaVideoModelV5(bypassCtx, p, probeEpisodeID); m != nil {
				c.noteChannelOutcome(bypassCtx, channelVideoModelV5, m, nil)
				pb = m
			} else if m, _ := c.resolvePlaybackViaTabMulti(bypassCtx, p, probeEpisodeID); m != nil {
				c.noteChannelOutcome(bypassCtx, channelTabMulti, m, nil)
				pb = m
			} else if m, _ := c.resolvePlaybackViaMobileV5(bypassCtx, p, probeEpisodeID); m != nil {
				c.noteChannelOutcome(bypassCtx, channelV5, m, nil)
				pb = m
			} else if m, _ := c.resolvePlaybackViaPlayerV1(bypassCtx, p, probeEpisodeID); m != nil {
				c.noteChannelOutcome(bypassCtx, channelPlayerV1, m, nil)
				pb = m
			}
		}
	}
	res.LatencyMs = c.clock().Sub(start).Milliseconds()
	if pb != nil {
		res.OK = true
		res.MaxDefinition = maxDefinitionLabel(pb)
		if modeID == SigningModeLocal {
			c.recordLocalVerdict(true)
		}
		return res
	}
	res.ErrorKind = probeErrorKind(ctx, modeID, err)
	if modeID == SigningModeLocal {
		// 快速实测没取到不等于本地不可用：判定交给后台完整复核。
		c.confirmLocalOrFlipAsync()
	}
	return res
}

func probeErrorKind(ctx context.Context, modeID int32, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if err == nil || errors.Is(err, ErrAnonymousLimited) {
		return "empty"
	}
	if modeID != SigningModeLocal {
		// 远程方式的硬错误都出在远程服务这条路上（连不上/签名失败）。
		return "unreachable"
	}
	return "error"
}

// maxDefinitionLabel 取模型里最高一档的画质标签，如 "1080P"。
// 口径与接口自身的 definition 一致：标签数字是画面的短边——横屏取高、
// 竖屏取宽（接口把 1080×1920 的一档标作 "1080p"，而非按高度的 1920）。
func maxDefinitionLabel(pb *model.Playback) string {
	best := 0
	for _, s := range pb.Streams {
		if v := streamDefinitionValue(s); v > best {
			best = v
		}
	}
	if best == 0 {
		return ""
	}
	return fmt.Sprintf("%dP", best)
}

// streamDefinitionValue 是单档流的画质数值：宽高齐全取短边，
// 缺一取其一，再缺解析 definition 标签开头的数字（"1080p" → 1080）。
func streamDefinitionValue(s model.Stream) int {
	if s.Width > 0 && s.Height > 0 {
		if s.Width < s.Height {
			return s.Width
		}
		return s.Height
	}
	if s.Height > 0 {
		return s.Height
	}
	if s.Width > 0 {
		return s.Width
	}
	var h int
	if _, err := fmt.Sscanf(s.Definition, "%dp", &h); err == nil {
		return h
	}
	return 0
}
