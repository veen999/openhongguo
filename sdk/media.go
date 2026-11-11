package sdk

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"

	"github.com/beilunyang/openhongguo/internal/drm"
	"github.com/beilunyang/openhongguo/internal/protocol"
	"github.com/beilunyang/openhongguo/internal/protocol/sign"
	"github.com/beilunyang/openhongguo/sdk/model"
)

// PlaybackOptions 预留给码流规格或接口的选择。
// 第二阶段按服务端下发的原样解析模型，由调用方挑选流。
type PlaybackOptions struct{}

// ResolvePlayback 加载一集的播放模型：各清晰度的地址、编码和码率。
//
// 请求就是官方客户端发出的、带签名的 video_model 调用。
// 响应是客户端的 TTVideoEngine 模型。受保护的模型会和 ErrDRMProtected
// 一起返回；把它传给 ContentKey 取得密钥。
// 模型携带的是包装后的密钥材料（Playback.KeyMaterial），从不包含内容密钥本身、
// 分片校验数据或解密后的媒体。
func (c *Client) ResolvePlayback(ctx context.Context, episodeID string, _ PlaybackOptions) (*model.Playback, error) {
	return withFreshDevice(ctx, c, true, func(ctx context.Context) (*model.Playback, error) {
		if !validID(episodeID) {
			return nil, fmt.Errorf("invalid episode ID")
		}
		// Issuance channels, in order. Since 2026-11-10/11 the singular
		// reads return empty for anonymous guests; the multi reads are
		// the paths that still issue, and they run on a dedicated
		// in-memory playback identity (registered the current way, see
		// playback_identity.go): player multi first, then the video_tab
		// multi. When both come back empty the identity is dropped, a
		// fresh one is registered and the tab multi is retried once —
		// playback identities can be single-use. The singular channels
		// on the stored catalog guest stay as final fallbacks.
		// A channel that produced a model (err may be ErrDRMProtected)
		// ends the chain; empty answers fall through.
		if pb, err := c.playbackIdentity(ctx); err == nil {
			if playback, err := c.resolvePlaybackViaMultiPlayer(ctx, pb, episodeID); playback != nil {
				return playback, err
			}
			if playback, err := c.resolvePlaybackViaTabMulti(ctx, pb, episodeID); playback != nil {
				return playback, err
			}
			c.dropPlaybackIdentityIf(pb)
			if pb2, err := c.playbackIdentity(ctx); err == nil {
				if playback, err := c.resolvePlaybackViaTabMulti(ctx, pb2, episodeID); playback != nil {
					return playback, err
				}
			}
		}
		if playback, err := c.resolvePlaybackViaTab(ctx, episodeID); playback != nil {
			return playback, err
		}
		return c.resolvePlaybackViaPlayer(ctx, episodeID)
	})
}

// playbackHeaders builds the header set for the multi playback
// channels from the signing headers: the current generation's UA and
// store-region headers, and none of the catalog extras (no guest
// cookie, no lc / x-reading-request, no bdturing placeholder).
func playbackHeaders(signHeaders map[string]string, device model.Device, versionCode string) map[string]string {
	h := make(map[string]string, len(signHeaders)+3)
	for k, v := range signHeaders {
		h[k] = v
	}
	h["User-Agent"] = protocol.UserAgentCronet(device, versionCode)
	h["x-ss-dp"] = protocol.AID
	h["x-tt-store-region"] = "cn"
	h["x-tt-store-region-src"] = "did"
	delete(h, "x-vc-bdturing-sdk-version")
	return h
}

// resolvePlaybackViaMultiPlayer fetches the playback model through
// POST /novel/player/multi_video_model/v1/ on the B host: the seriessdk
// batch request (one episode), gzipped, signed on the medusa branch the
// channel accepts (branch 0), under the PlaybackVersion profile,
// presented by the dedicated playback identity p.
func (c *Client) resolvePlaybackViaMultiPlayer(ctx context.Context, p *DeviceProfile, episodeID string) (*model.Playback, error) {
	body, err := protocol.MultiPlaybackBody(episodeID)
	if err != nil {
		return nil, err
	}
	gz, err := gzipBytes(body)
	if err != nil {
		return nil, err
	}
	ticket := c.clock().UnixMilli()
	rawQuery, headers := sign.SignPOSTBranch(protocol.PathPlaybackMulti, protocol.PlaybackMultiPairs(c.identity(p)), ticket, p.DeviceID, protocol.PlaybackVersionName, gz, nil, 0)
	h := playbackHeaders(headers, c.deviceOf(p), protocol.PlaybackVersionCode)
	h["Content-Encoding"] = "gzip"
	raw, _, err := c.doRaw(ctx, "POST", c.multiOrigin()+protocol.PathPlaybackMulti+"?"+rawQuery, h, gz)
	if err != nil {
		return nil, err
	}
	return finishMultiPlayback(raw, episodeID)
}

// resolvePlaybackViaTabMulti fetches the playback model through
// GET /reading/bookapi/video_tab/multi_video_model/v1/ on the B host,
// under the TabVersion profile, branch-0 signed, presented by the
// dedicated playback identity p.
func (c *Client) resolvePlaybackViaTabMulti(ctx context.Context, p *DeviceProfile, episodeID string) (*model.Playback, error) {
	ticket := c.clock().UnixMilli()
	rawQuery, headers := sign.SignGETBranch(protocol.SignedVideoTabMultiPairs(c.identity(p), episodeID), ticket, p.DeviceID, protocol.TabVersionName, nil, 0)
	h := playbackHeaders(headers, c.deviceOf(p), protocol.TabVersionCode)
	raw, _, err := c.doRaw(ctx, "GET", c.multiOrigin()+protocol.PathVideoTabMulti+"?"+rawQuery, h, nil)
	if err != nil {
		return nil, err
	}
	return finishMultiPlayback(raw, episodeID)
}

// finishMultiPlayback parses a multi-channel response: the model sits
// under data.video_model_data[<episode>] (an object shaped like the
// singular data), or directly at data.video_model.
func finishMultiPlayback(raw []byte, episodeID string) (*model.Playback, error) {
	playback, err := parseMultiPlayback(raw, episodeID)
	if err != nil {
		return nil, err
	}
	if playback.Protected {
		return playback, ErrDRMProtected
	}
	return playback, nil
}

func parseMultiPlayback(raw []byte, episodeID string) (*model.Playback, error) {
	var probe struct {
		Data struct {
			VideoModel     json.RawMessage            `json:"video_model"`
			VideoModelData map[string]json.RawMessage `json:"video_model_data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	if len(probe.Data.VideoModel) > 0 {
		return protocol.ParsePlayback(raw, episodeID)
	}
	entry, ok := probe.Data.VideoModelData[episodeID]
	if !ok || len(entry) == 0 {
		return nil, fmt.Errorf("multi playback response has no model for episode %s", episodeID)
	}
	wrapped := make([]byte, 0, len(entry)+24)
	wrapped = append(wrapped, `{"code":0,"data":`...)
	wrapped = append(wrapped, entry...)
	wrapped = append(wrapped, '}')
	return protocol.ParsePlayback(wrapped, episodeID)
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// resolvePlaybackViaTab fetches the playback model through
// GET /reading/bookapi/video_tab/video_model/v with the search guest
// identity. The response envelope is identical to the player channel's,
// so ParsePlayback handles it unchanged.
func (c *Client) resolvePlaybackViaTab(ctx context.Context, episodeID string) (*model.Playback, error) {
	p, err := c.ensureSearchGuest(ctx)
	if err != nil {
		return nil, err
	}
	ticket := c.clock().UnixMilli()
	rawQuery, headers := sign.SignGET(protocol.SignedVideoTabPairs(c.identity(p), episodeID), ticket, p.DeviceID, protocol.VersionName, nil)
	raw, _, err := c.doRaw(ctx, "GET", c.apiOrigin+protocol.PathVideoTabModel+"?"+rawQuery, anonymousHeaders(c, p, ticket, headers), nil)
	if err != nil {
		return nil, err
	}
	playback, err := protocol.ParsePlayback(raw, episodeID)
	if err != nil {
		return nil, err
	}
	if playback.Protected {
		return playback, ErrDRMProtected
	}
	return playback, nil
}

// resolvePlaybackViaPlayer is the original signed video_model POST.
func (c *Client) resolvePlaybackViaPlayer(ctx context.Context, episodeID string) (*model.Playback, error) {
	p, err := c.ensureSearchGuest(ctx)
	if err != nil {
		return nil, err
	}
	body, err := protocol.PlaybackBody(episodeID)
	if err != nil {
		return nil, err
	}
	ticket := c.clock().UnixMilli()
	rawQuery, headers := sign.SignPOST(protocol.PathPlayback, protocol.DeviceQueryPairs(c.identity(p)), ticket, p.DeviceID, protocol.VersionName, body, nil)
	raw, _, err := c.doRaw(ctx, "POST", c.apiOrigin+protocol.PathPlayback+"?"+rawQuery, anonymousHeaders(c, p, ticket, headers), body)
	if err != nil {
		return nil, err
	}
	playback, err := protocol.ParsePlayback(raw, episodeID)
	if err != nil {
		return nil, err
	}
	if playback.Protected {
		return playback, ErrDRMProtected
	}
	return playback, nil
}

// ContentKey 从播放模型算出加密分集的内容密钥。
// 播放模型就是 ResolvePlayback 连同 ErrDRMProtected 一起返回的那个。这是纯本地计算：
// 按官方播放器的方式从模型的 KeyMaterial 解包出密钥，不再发出任何请求。
// 模型的所有流共用这个密钥（同一集的每种清晰度都带有相同的 spade_a 和 kid，
// 2026-10-24 观察）。
//
// 未加密的模型返回 ErrNotProtected；当前版本无法解包的密钥材料返回
// ErrDRMProtected。
func ContentKey(pb *model.Playback) (model.ContentKey, error) {
	if pb == nil {
		return model.ContentKey{}, fmt.Errorf("playback model is missing")
	}
	if !pb.Protected {
		return model.ContentKey{}, ErrNotProtected
	}
	k, err := drm.Derive(pb.KeyMaterial)
	if err != nil {
		return model.ContentKey{}, fmt.Errorf("%w: %v", ErrDRMProtected, err)
	}
	return model.ContentKey{Hex: k.Hex, Version: k.Version}, nil
}
