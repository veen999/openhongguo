package sdk

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
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
	mode := c.effectiveSigningMode()
	playback, err := c.resolvePlaybackChain(ctx, episodeID, mode)
	if playback != nil {
		c.noteResolveSuccess(mode, episodeID)
		return playback, err
	}
	// 算法策略层（镜像 hongguoTV 的 PlaybackAlgorithmPolicy）：用户选
	// 的是本地、本次也是按本地取的，连续失败满两次即把实际生效方式
	// 切到远程计算-unidbg，并用新方式重试本次取址；远程方式成功后
	// 的本地恢复探测见 noteResolveSuccess。
	if c.signingMode() == SigningModeLocal && mode == SigningModeLocal && c.noteLocalFailure() {
		if playback, err := c.resolvePlaybackChain(ctx, episodeID, SigningModeUnidbg); playback != nil {
			c.noteResolveSuccess(SigningModeUnidbg, episodeID)
			return playback, err
		} else if err != nil {
			return nil, err
		}
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
		if pb, err := c.playbackIdentity(ctx); err == nil {
			if playback, err := c.resolvePlaybackViaMobileV5(ctx, pb, episodeID); playback != nil {
				return playback, err
			}
			// 2026-11-20 实测：平台逐通道开关播放发放——v5 全天空返的
			// 时段里 tab 多集仍在发放（满院亲戚/少夫人3/冒名入仕等均经
			// 此通道拿到 5 档模型）。故 tab 多集紧随 v5，不再排在已退役
			// 的 player/multi 之后白等。
			if playback, err := c.resolvePlaybackViaTabMulti(ctx, pb, episodeID); playback != nil {
				return playback, err
			}
		}
		if playback, err := c.resolvePlaybackViaPlayer(ctx, episodeID); playback != nil {
			return playback, err
		}
		if pb, err := c.playbackIdentity(ctx); err == nil {
			if playback, err := c.resolvePlaybackViaMultiPlayerV2(ctx, pb, episodeID); playback != nil {
				return playback, err
			}
			if playback, err := c.resolvePlaybackViaMultiPlayer(ctx, pb, episodeID); playback != nil {
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
		if remoteErr != nil {
			return nil, remoteErr
		}
		return c.resolvePlaybackViaPlayer(ctx, episodeID)
	})
}

// mobileV5UA is the User-Agent the v5 playback request carries: the
// official 7.3.9.32 client on the PGT-AN10 profile the v5 query
// presents (the query itself pins 7.4.3.32; the mismatch is the
// published protocol's own and is kept verbatim).
const mobileV5UA = "com.phoenix.read/73932 (Linux; U; Android 12; zh_CN_#Hans; PGT-AN10; Build/V417IR;tt-ok/3.12.13.20)"

// resolvePlaybackViaMobileV5 fetches the playback model through the
// mobile v5 channel: POST /novel/player/multi_video_model/player/v1
// on the A host, gzipped body, the reduced official-mobile query
// profile (7.4.3.32 / vivo_8662_64 / PGT-AN10) carrying only the
// playback identity's device_id / iid / cdid, signed with
// sign.SignPlaybackV5 (x-khronos / x-helios / x-medusa only — the old
// signature headers are forgery markers under v5). No cookie rides
// along. The model sits at data[<content type>][episode].video_model.
//
// mixed_video_id_map is keyed by the episode's content type: 1004 for
// regular series (short plays and comics alike), 1001 for PUGC
// episodes (UGC album entries). ResolvePlayback only receives an
// episode id, so the id is filed under both keys in one request —
// the server answers under the key that matches the episode and
// ignores the other (verified live for both kinds).
func (c *Client) resolvePlaybackViaMobileV5(ctx context.Context, p *DeviceProfile, episodeID string) (*model.Playback, error) {
	body := []byte(fmt.Sprintf(`{"biz_param":{"detail_page_version":0,"device_level":1,"disable_digg_stat":false,"disable_video_relate_book":false,"need_all_video_definition":true,"need_mp4_align":false,"use_os_player":false,"use_server_dns":false,"video_platform":1024},"mixed_video_id_map":{"1004":["%s"],"1001":["%s"]},"dr_scene":"player"}`, episodeID, episodeID))
	gz, err := gzipBytes(body)
	if err != nil {
		return nil, err
	}
	pairs := [][2]string{
		{"aid", protocol.AID},
		{"app_name", "novelread"},
		{"version_code", "74332"},
		{"version_name", "7.4.3.32"},
		{"channel", "vivo_8662_64"},
		{"device_platform", "android"},
		{"device_type", "PGT-AN10"},
		{"os_version", "12"},
		{"update_version_code", "74332"},
		{"iid", p.InstallID},
		{"cdid", p.CDID},
		{"device_id", p.DeviceID},
	}
	ticket := c.clock().UnixMilli()
	rawQuery, headers := sign.SignPlaybackV5(pairs, ticket, sign.V5Identity{
		DeviceID:    p.DeviceID,
		VersionName: "7.4.3.32",
		Channel:     "vivo_8662_64",
		OSVersion:   "12",
		DeviceType:  "PGT-AN10",
	}, gz, nil)
	h := make(map[string]string, len(headers)+3)
	for k, v := range headers {
		h[k] = v
	}
	h["User-Agent"] = mobileV5UA
	h["Accept"] = "application/json"
	h["Content-Encoding"] = "gzip"
	raw, _, err := c.doRaw(ctx, "POST", c.apiOrigin+protocol.PathPlaybackMobileV5+"?"+rawQuery, h, gz)
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
