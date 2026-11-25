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

// ProbeSigningMode 检测一种计算方式当前是否可用：对固定探针剧集按
// 该方式的真实播放口径完整取址一次。本地与 unidbg 跑完整取址链
// （resolvePlaybackChain：全通道依次落空才算失败，含新鲜身份重试；
// unidbg 期间临时把生效方式切到 unidbg 让传输层重签，测完立即恢复），
// frida 走远程真机通道全程（tab GET 全灭后还有服务签名的 POST 播放
// 路由兜底）。
//
// 旧实现只在当前身份上单发 tab 多集与 v5 两条通道，与真实播放口径
// 不一致：新身份尚在平台试用期、或发放只开在播放器 POST 通道上时，
// 单发全空但完整链磨过去能出流，检测却报失败（2026-11-25 用户实测
// 弹层报本地/unidbg 失败、同环境完整链 5 档出流）。检测预算因此按
// 链时长放宽：本地 180 秒、unidbg 90 秒、frida 60 秒。检测不改锚点、
// 不触发策略层的自动切换确认；完整链在全灭时可能换一次播放身份，
// 这是与真实播放一致的行为。
func (c *Client) ProbeSigningMode(ctx context.Context, mode string) SigningModeProbe {
	res := SigningModeProbe{Mode: "local"}
	var modeID int32 = SigningModeLocal
	switch mode {
	case "unidbg":
		modeID, res.Mode = SigningModeUnidbg, "unidbg"
	case "frida":
		modeID, res.Mode = SigningModeFrida, "frida"
	}
	budget := 180 * time.Second
	switch modeID {
	case SigningModeUnidbg:
		budget = 90 * time.Second
	case SigningModeFrida:
		budget = 60 * time.Second
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
		// 与 ResolvePlayback 的本地路径同口径：完整链全灭先经多通道
		// 复核（含试用期同身份复探——沙箱实测单跑一遍链 13 秒全灭、
		// 复核养过试用期后第二遍链即出 5 档），复核称可用才跑第二遍。
		bypassCtx := withResignBypass(ctx)
		pb, err = c.resolvePlaybackChain(bypassCtx, probeEpisodeID, SigningModeLocal)
		if pb == nil && c.localChannelsAlive(bypassCtx, probeEpisodeID) {
			pb, err = c.resolvePlaybackChain(bypassCtx, probeEpisodeID, SigningModeLocal)
		}
	}
	res.LatencyMs = c.clock().Sub(start).Milliseconds()
	if pb != nil {
		res.OK = true
		res.MaxDefinition = maxDefinitionLabel(pb)
		if modeID == SigningModeLocal {
			// 链路级检测与播放同口径，结果直接喂判定缓存。
			c.recordLocalVerdict(true)
		}
		return res
	}
	res.ErrorKind = probeErrorKind(ctx, modeID, err)
	if modeID == SigningModeLocal && res.ErrorKind != "timeout" {
		c.recordLocalVerdict(false)
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
func maxDefinitionLabel(pb *model.Playback) string {
	best := 0
	for _, s := range pb.Streams {
		if s.Height > best {
			best = s.Height
		}
	}
	if best == 0 {
		for _, s := range pb.Streams {
			var h int
			if _, err := fmt.Sscanf(s.Definition, "%dp", &h); err == nil && h > best {
				best = h
			}
		}
	}
	if best == 0 {
		return ""
	}
	return fmt.Sprintf("%dP", best)
}
