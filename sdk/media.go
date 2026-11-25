package sdk

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/beilunyang/openhongguo/internal/drm"
	"github.com/beilunyang/openhongguo/internal/protocol"
	"github.com/beilunyang/openhongguo/internal/protocol/sign"
	"github.com/beilunyang/openhongguo/sdk/model"
)

// readingRequestTicket is this process's x-reading-request value: the
// shipped client's response-integrity interceptor (SecurityHelper)
// stamps every RPC request with "<process start millis>-<random int>"
// and the server echoes it in x-reading-response. One value per
// process, generated once.
var readingRequestTicket = func() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	n := int64(int32(binary.BigEndian.Uint32(b[:])))
	if n < 0 {
		n = -n
	}
	return fmt.Sprintf("%d-%d", time.Now().UnixMilli(), n)
}()

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
	// 锚点不是本地（用户选了远程）：按生效方式直跑全链，无策略层。
	if c.signingMode() != SigningModeLocal {
		mode := c.effectiveSigningMode()
		playback, err := c.resolvePlaybackChain(ctx, episodeID, mode)
		if playback != nil {
			c.noteResolveSuccess(mode, episodeID)
		}
		return playback, err
	}
	// 锚点本地 + 判定「确认不可用」未过期：本地零等待，本次直接走
	// unidbg（生效方式已由 recordLocalVerdict 同步为 unidbg，传输层
	// 据此重签）。unidbg 也失败时判定可能已失真，限时补试本地一遍。
	if verdict, fresh := c.localVerdictFresh(); fresh && verdict == localVerdictDown {
		playback, err := c.resolvePlaybackChain(ctx, episodeID, SigningModeUnidbg)
		if playback != nil {
			c.noteResolveSuccess(SigningModeUnidbg, episodeID)
			return playback, err
		}
		boxCtx, cancel := context.WithTimeout(ctx, localPlayAttemptTimeout)
		playback, err = c.resolvePlaybackChain(boxCtx, episodeID, SigningModeLocal)
		cancel()
		if playback != nil {
			c.noteResolveSuccess(SigningModeLocal, episodeID)
		}
		return playback, err
	}
	// 判定「可用」：本地全链（不限时，可用判定下通道应快速发放）。
	// 判定未知/过期：本地限时一试——死时段全链长尾被 20 秒封顶，
	// 试用期等慢恢复不在播放路径上等，交给后台复核养判定。
	verdict, fresh := c.localVerdictFresh()
	playCtx := ctx
	var cancel context.CancelFunc
	if !fresh || verdict != localVerdictAlive {
		playCtx, cancel = context.WithTimeout(ctx, localPlayAttemptTimeout)
	}
	playback, err := c.resolvePlaybackChain(playCtx, episodeID, SigningModeLocal)
	if cancel != nil {
		cancel()
	}
	if playback != nil {
		c.noteResolveSuccess(SigningModeLocal, episodeID)
		return playback, err
	}
	// 本地这次没拿到：不在播放路径上做多通道复核（旧实现内联复核，
	// 死时段转圈一分多钟还不切远程）。记 45 秒临时判定 + 触发后台
	// 完整复核（确认判定随后覆盖），本次立即切 unidbg 顶上（播放不
	// 出现失败界面）；远程成功后的本地恢复探测见 noteResolveSuccess。
	c.recordLocalProvisionalDown()
	c.confirmLocalOrFlipAsync()
	playback, err = c.resolvePlaybackChain(ctx, episodeID, SigningModeUnidbg)
	if playback != nil {
		c.noteResolveSuccess(SigningModeUnidbg, episodeID)
		return playback, err
	} else if err != nil {
		return nil, err
	}
	return playback, err
}

// resolvePlaybackChain 按给定计算方式跑一遍完整取址链：远程方式先行
// 远程签名取址、本地全链兜底；本地方式只跑本地全链。
func (c *Client) resolvePlaybackChain(ctx context.Context, episodeID string, mode int32) (*model.Playback, error) {
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
		// The mobile v5 channel goes first: on 2026-11-16 the
		// platform retired every previous playback chain (all of them
		// answer empty for every identity now), and the official
		// client's v04.10.03 protocol — POST multi_video_model/
		// player/v1, gzipped body, khronos/helios/medusa-v5 signing —
		// is the chain that issues (see resolvePlaybackViaMobileV5).
		// The older channels stay behind it as fallbacks in case the
		// platform rolls back.
		// frida 方式（服务自带身份）先走远程签名取址（见 remote_sign.go）：
		// 取到模型即返回；远程失败先记下原因，本地链照常兜底，本地也
		// 失败时把远程的失败原因报给用户（他选的方式出了什么问题）。
		// unidbg 方式不在这里分流：它由传输层对本链的播放请求统一重签
		//（见 resign_transport.go），链本身与本地完全一致。
		var remoteErr error
		if mode == SigningModeFrida {
			if playback, err := c.resolvePlaybackViaRemoteMode(ctx, episodeID, mode); playback != nil {
				return playback, err
			} else if err != nil {
				remoteErr = err
			}
		}
		// 本轮计划按通道健康定（见 channel_health.go）：首选通道在它
		// 所在的结构段内置前，down 中的通道跳过其常规位置；全部通道
		// 都 down 时不跳过、按经典顺序整链重探。计划是链首快照，本轮
		// 内新记的健康变化不截断本轮结构（新鲜身份重试要完整走完）。
		plan := c.channels.plan(c.clock())
		// 2026-11-20 实测：平台逐通道开关播放发放——v5 全天空返的
		// 时段里 tab 多集仍在发放（满院亲戚/少夫人3/冒名入仕等均经
		// 此通道拿到 5 档模型）。故 tab 多集紧随 v5，不再排在已退役
		// 的 player/multi 之后白等。
		head := []identityChannel{
			{channelV5, c.resolvePlaybackViaMobileV5},
			{channelTabMulti, c.resolvePlaybackViaTabMulti},
		}
		if plan.preferred == channelTabMulti {
			head[0], head[1] = head[1], head[0]
		}
		if plan.attempt(channelV5) || plan.attempt(channelTabMulti) {
			if pb, err := c.playbackIdentity(ctx); err == nil {
				for _, ch := range head {
					if playback, err := c.tryPlaybackChannel(ctx, plan, ch.name, func() (*model.Playback, error) {
						return ch.fn(ctx, pb, episodeID)
					}); playback != nil {
						return playback, err
					}
				}
			}
		}
		if playback, err := c.tryPlaybackChannel(ctx, plan, channelPlayer, func() (*model.Playback, error) {
			return c.resolvePlaybackViaPlayer(ctx, episodeID)
		}); playback != nil {
			return playback, err
		}
		if pb, err := c.playbackIdentity(ctx); err == nil {
			multi := []identityChannel{
				{channelMultiV2, c.resolvePlaybackViaMultiPlayerV2},
				{channelMulti, c.resolvePlaybackViaMultiPlayer},
			}
			if plan.preferred == channelMulti {
				multi[0], multi[1] = multi[1], multi[0]
			}
			for _, ch := range multi {
				if playback, err := c.tryPlaybackChannel(ctx, plan, ch.name, func() (*model.Playback, error) {
					return ch.fn(ctx, pb, episodeID)
				}); playback != nil {
					return playback, err
				}
			}
			// multi 通道全空（或被健康计划跳过）后照旧丢弃播放身份、
			// 换新鲜身份重试一次 tab 多集：播放身份可能是一次性的，
			// 且 tab 多集的 down 标记可能记在旧身份头上——这次重试不
			// 受健康计划跳过，结果照常记账。
			c.dropPlaybackIdentityIf(pb)
			if pb2, err := c.playbackIdentity(ctx); err == nil {
				if playback, err := c.attemptPlaybackChannel(ctx, channelTabMulti, func() (*model.Playback, error) {
					return c.resolvePlaybackViaTabMulti(ctx, pb2, episodeID)
				}); playback != nil {
					return playback, err
				}
			}
		}
		if playback, err := c.tryPlaybackChannel(ctx, plan, channelTab, func() (*model.Playback, error) {
			return c.resolvePlaybackViaTab(ctx, episodeID)
		}); playback != nil {
			return playback, err
		}
		if remoteErr != nil {
			return nil, remoteErr
		}
		// 收尾的 player 尝试不受健康计划跳过：它的错误是整条链的
		// 错误来源（如 ErrAnonymousLimited 驱动换设备重试），跳过
		// 会让链以 (nil, nil) 收场。
		return c.attemptPlaybackChannel(ctx, channelPlayer, func() (*model.Playback, error) {
			return c.resolvePlaybackViaPlayer(ctx, episodeID)
		})
	})
}

// identityChannel 是走播放身份的一条取址通道：健康记账用的通道名与
// 通道实现。
type identityChannel struct {
	name string
	fn   func(context.Context, *DeviceProfile, string) (*model.Playback, error)
}

// attemptPlaybackChannel 执行一次通道尝试并把结果记进通道健康跟踪。
func (c *Client) attemptPlaybackChannel(ctx context.Context, name string, attempt func() (*model.Playback, error)) (*model.Playback, error) {
	playback, err := attempt()
	c.noteChannelOutcome(ctx, name, playback, err)
	return playback, err
}

// tryPlaybackChannel 是 attemptPlaybackChannel 的健康计划版：通道在
// 本轮计划里被跳过时不发请求、直接落空继续（返回 nil, nil）。
func (c *Client) tryPlaybackChannel(ctx context.Context, plan chainPlan, name string, attempt func() (*model.Playback, error)) (*model.Playback, error) {
	if !plan.attempt(name) {
		return nil, nil
	}
	return c.attemptPlaybackChannel(ctx, name, attempt)
}

// resolvePlaybackViaMobileV5 fetches the playback model through the
// v5-signer channel in the form the service currently accepts
// (upstream PR #23, live-verified 2026-11-23): POST
// /novel/player/multi_video_model/v1/ on the A host, PLAINTEXT body
// (device_level 4, no dr_scene), the full 7.4.3.32 query profile
// (protocol.PlaybackV5Pairs, ts filled by the signer), signed with
// the client's PlaybackV5Signer whose constants carry the
// v04.06.04 profile (x-khronos / x-helios / x-medusa + x-ss-stub).
// No cookie rides along. The model sits flat at
// data[<episode>].video_model (finishMobileV5Playback also accepts
// the older nested layouts).
func (c *Client) resolvePlaybackViaMobileV5(ctx context.Context, p *DeviceProfile, episodeID string) (*model.Playback, error) {
	body, err := protocol.PlaybackV5Body(episodeID)
	if err != nil {
		return nil, err
	}
	ticket := c.clock().UnixMilli()
	rawQuery, headers, err := c.playbackSigner.Sign(protocol.PlaybackV5Pairs(c.identity(p)), ticket, body, nil)
	if err != nil {
		return nil, err
	}
	h := make(map[string]string, len(headers)+3)
	for k, v := range headers {
		h[k] = v
	}
	h["User-Agent"] = protocol.PlaybackUserAgent(c.deviceOf(p))
	h["Accept"] = "application/json; charset=utf-8,application/x-protobuf"
	h["x-xs-from-web"] = "0"
	raw, _, err := c.doRaw(ctx, "POST", c.apiOrigin+protocol.PathPlaybackV5+"?"+rawQuery, h, body)
	if err != nil {
		return nil, err
	}
	return finishMobileV5Playback(raw, episodeID)
}

// finishMobileV5Playback parses a v5-channel response: the model sits
// under data[<content type>][<episode>].video_model (an object or a
// JSON string), keyed 1004 for regular series and 1001 for PUGC
// episodes; some responses flatten the id map directly under data.
func finishMobileV5Playback(raw []byte, episodeID string) (*model.Playback, error) {
	var probe struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	entries := probe.Data
	for _, key := range []string{"1004", "1001"} {
		bucket, ok := probe.Data[key]
		if !ok {
			continue
		}
		var byID map[string]json.RawMessage
		if err := json.Unmarshal(bucket, &byID); err != nil {
			return nil, err
		}
		if _, has := byID[episodeID]; has {
			entries = byID
			break
		}
	}
	entry, ok := entries[episodeID]
	if !ok || len(entry) == 0 {
		return nil, fmt.Errorf("mobile v5 playback response has no entry for episode %s", episodeID)
	}
	var holder struct {
		VideoModel json.RawMessage `json:"video_model"`
	}
	if err := json.Unmarshal(entry, &holder); err != nil {
		return nil, err
	}
	if len(holder.VideoModel) == 0 {
		return nil, fmt.Errorf("mobile v5 playback response has no model for episode %s", episodeID)
	}
	wrapped := make([]byte, 0, len(holder.VideoModel)+40)
	wrapped = append(wrapped, `{"code":0,"data":{"video_model":`...)
	wrapped = append(wrapped, holder.VideoModel...)
	wrapped = append(wrapped, `}}`...)
	playback, err := protocol.ParsePlayback(wrapped, episodeID)
	if err != nil {
		return nil, err
	}
	if playback.Protected {
		return playback, ErrDRMProtected
	}
	return playback, nil
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
	h["x-reading-request"] = readingRequestTicket
	h["x-tt-store-region"] = "cn"
	h["x-tt-store-region-src"] = "did"
	if versionCode == protocol.PlaybackVersionCode {
		h["passport-sdk-version"] = "5051452"
	} else {
		h["passport-sdk-version"] = "50561"
	}
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
	if p.DeviceToken != "" {
		h["x-tt-dt"] = p.DeviceToken
	}
	h["Content-Encoding"] = "gzip"
	raw, _, err := c.doRaw(ctx, "POST", c.multiOrigin()+protocol.PathPlaybackMulti+"?"+rawQuery, h, gz)
	if err != nil {
		return nil, err
	}
	return finishMultiPlayback(raw, episodeID)
}

// resolvePlaybackViaMultiPlayerV2 fetches the playback model through
// POST /novel/player/multi_video_model/<dr_scene>/v1/ on the B host —
// the seriessdk V2 batch read the shipped client's immersive player
// and download line actually call (V1 multi is its in-method backup).
// Same signing and identity as the V1 multi channel; the scene rides
// in the path and in the body's dr_scene.
func (c *Client) resolvePlaybackViaMultiPlayerV2(ctx context.Context, p *DeviceProfile, episodeID string) (*model.Playback, error) {
	const scene = "default"
	body, err := protocol.MultiPlaybackBodyV2(episodeID, scene)
	if err != nil {
		return nil, err
	}
	gz, err := gzipBytes(body)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf(protocol.PathPlaybackMultiV2Fmt, scene)
	ticket := c.clock().UnixMilli()
	rawQuery, headers := sign.SignPOSTBranch(path, protocol.PlaybackMultiPairs(c.identity(p)), ticket, p.DeviceID, protocol.PlaybackVersionName, gz, nil, 0)
	h := playbackHeaders(headers, c.deviceOf(p), protocol.PlaybackVersionCode)
	if p.DeviceToken != "" {
		h["x-tt-dt"] = p.DeviceToken
	}
	h["Content-Encoding"] = "gzip"
	raw, _, err := c.doRaw(ctx, "POST", c.multiOrigin()+path+"?"+rawQuery, h, gz)
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
	if p.DeviceToken != "" {
		h["x-tt-dt"] = p.DeviceToken
	}
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
	raw, _, err := c.doRaw(ctx, "GET", c.multiOrigin()+protocol.PathVideoTabModel+"?"+rawQuery, anonymousHeaders(c, p, ticket, headers), nil)
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
// Since 2026-11-10 the endpoint validates real x-argus / x-ladon and
// rejects the request when x-medusa rides along, so it is signed with
// SignPlaybackPOST (Argus 551 envelope + Ladon, no Medusa) — the fix
// from upstream PR beilunyang/openhongguo#7 (bed1459), live-verified
// there against this same endpoint.
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
	rawQuery, headers := sign.SignPlaybackPOST(protocol.PathPlayback, protocol.DeviceQueryPairs(c.identity(p)), ticket, p.DeviceID, protocol.VersionName, body, nil)
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

// playbackRouteUnavailable 判断播放路由是否在服务端不存在（404/405）；
// 随上游 v0.4.0 合并保留，供取址链的兼容判断使用。
func playbackRouteUnavailable(err error) bool {
	var api *APIError
	return errors.As(err, &api) && (api.HTTPStatus == 404 || api.HTTPStatus == 405)
}
