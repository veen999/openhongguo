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

// ProbeSigningMode 检测一种计算方式当前是否可用：对固定探针剧集真实
// 取址一次。本地与 unidbg 在当前播放身份上顺序单发 tab 多集与 v5 两条
// 主力通道（unidbg 期间临时把生效方式切到 unidbg 让传输层重签，测完
// 立即恢复）；frida 走远程真机通道。只读检测：不改锚点、不触发策略层
// 的自动切换确认，也不烧新身份。
func (c *Client) ProbeSigningMode(ctx context.Context, mode string) SigningModeProbe {
	res := SigningModeProbe{Mode: "local"}
	var modeID int32 = SigningModeLocal
	switch mode {
	case "unidbg":
		modeID, res.Mode = SigningModeUnidbg, "unidbg"
	case "frida":
		modeID, res.Mode = SigningModeFrida, "frida"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := c.clock()
	var pb *model.Playback
	var err error
	switch modeID {
	case SigningModeFrida:
		pb, err = c.resolvePlaybackViaRemoteMode(withResignBypass(ctx), probeEpisodeID, SigningModeFrida)
	case SigningModeUnidbg:
		old := c.effSignMode.Swap(SigningModeUnidbg)
		pb, err = c.probeTwoChannelsModel(ctx, probeEpisodeID)
		c.effSignMode.Store(old)
	default:
		pb, err = c.probeTwoChannelsModel(withResignBypass(ctx), probeEpisodeID)
	}
	res.LatencyMs = c.clock().Sub(start).Milliseconds()
	if pb != nil {
		res.OK = true
		res.MaxDefinition = maxDefinitionLabel(pb)
		return res
	}
	res.ErrorKind = probeErrorKind(ctx, modeID, err)
	return res
}

// probeTwoChannelsModel 与 probeTwoChannels 同口径（当前身份上顺序单发
// tab 多集 → v5），但返回先拿到的播放模型本身，供检测展示最高画质。
func (c *Client) probeTwoChannelsModel(ctx context.Context, episodeID string) (*model.Playback, error) {
	p, err := c.playbackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if pb, _ := c.resolvePlaybackViaTabMulti(ctx, p, episodeID); pb != nil {
		c.noteChannelOutcome(ctx, channelTabMulti, pb, nil)
		return pb, nil
	}
	pb, err := c.resolvePlaybackViaMobileV5(ctx, p, episodeID)
	if pb != nil {
		c.noteChannelOutcome(ctx, channelV5, pb, nil)
	}
	return pb, err
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
