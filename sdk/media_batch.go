package sdk

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/beilunyang/openhongguo/internal/protocol"
	"github.com/beilunyang/openhongguo/sdk/model"
)

// batchV5ChunkSize 是一次移动端 v5 批量请求最多携带的集数。响应按每集
// 约 19KB 估算，20 集约 380KB，仍在单次响应可接受的范围内。
const batchV5ChunkSize = 20

// ResolvePlaybackBatch 批量解析多集的播放模型。优先走移动端 v5 通道
// 一次请求多集（同一播放身份复用，不逐集注册）；批量响应未覆盖的集
// 回退到单集 ResolvePlayback 的完整通道链。批量通道的健康独立记账
// （channel_health.go 的 v5batch）：一次空发放（空响应或零模型）后
// 45 秒内整块跳过批量 POST、直接走单集链，不再逐块白试死通道。
// 返回值是成功解析的播放模型与逐集错误：整体失败时，请求过的每一
// 集都会出现在 errs 里。
func (c *Client) ResolvePlaybackBatch(ctx context.Context, ids []string) (map[string]*model.Playback, map[string]error) {
	playbacks := make(map[string]*model.Playback)
	errs := make(map[string]error)
	seen := make(map[string]bool, len(ids))
	uniq := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return playbacks, errs
	}
	// frida 方式没有批量签名协议：逐集走单集解析（其内部同样是
	// 远程优先、本地兜底），结果按同一口径分桶。unidbg 方式走批量
	// v5 POST，传输层会像单集一样对它重签，不必降级。
	if c.effectiveSigningMode() == SigningModeFrida {
		for _, id := range uniq {
			pb, err := c.ResolvePlayback(ctx, id, PlaybackOptions{})
			if pb != nil {
				playbacks[id] = pb
			} else if err != nil {
				errs[id] = err
			} else {
				errs[id] = fmt.Errorf("no playback model for episode %s", id)
			}
		}
		return playbacks, errs
	}
	p, idErr := c.playbackIdentity(ctx)
	for start := 0; start < len(uniq); start += batchV5ChunkSize {
		end := start + batchV5ChunkSize
		if end > len(uniq) {
			end = len(uniq)
		}
		chunk := uniq[start:end]
		var missing []string
		switch {
		case idErr != nil:
			missing = chunk
		case c.effectiveSigningMode() == SigningModeLocal:
			// 本地计算：v5 批量端点已退役（2026-11-25 探针：两种签名
			// 形状皆空返），整块直接走单集链。unidbg 方式仍走批量
			// POST——传输层重签后由远程发放，不在此列。
			missing = chunk
		case c.channels.isDown(channelV5Batch, c.clock()):
			// 批量通道已知不发放：不再白付一次死 POST，整块直接走
			// 单集链（链本身按健康计划首选活通道）。
			missing = chunk
		default:
			raw, err := c.resolveBatchV5Raw(ctx, p, chunk)
			switch {
			case err != nil:
				// 200 空响应体在 doRaw 里映射为 ErrAnonymousLimited：
				// 这就是批量通道的空发放，记 down；其余错误（传输、
				// 上下文等）不归咎于通道，只回退本块。
				if errors.Is(err, ErrAnonymousLimited) {
					c.channels.recordEmpty(channelV5Batch, c.clock())
				}
				missing = chunk
			default:
				var got map[string]*model.Playback
				got, missing = parseBatchV5Playback(raw, chunk)
				if len(got) == 0 {
					// 响应回来了却一集模型都没有：批量通道空发放，
					// 记 down，后续块与后续调用不再白试。
					c.channels.recordEmpty(channelV5Batch, c.clock())
				} else {
					c.channels.recordSuccess(channelV5Batch, c.clock())
				}
				for id, pb := range got {
					playbacks[id] = pb
				}
			}
		}
		for _, id := range missing {
			pb, err := c.ResolvePlayback(ctx, id, PlaybackOptions{})
			if pb != nil {
				playbacks[id] = pb
			} else if err != nil {
				errs[id] = err
			} else {
				errs[id] = fmt.Errorf("no playback model for episode %s", id)
			}
		}
	}
	return playbacks, errs
}

// resolveBatchV5Raw 发一次携带多集的移动端 v5 请求，返回原始响应。
// 请求体与单集版式一致，只是 mixed_video_id_map 的两个列表各装多集；
// 服务端把每集的模型平铺在 data[<episode>] 下（2026-11-19 实测）。
func (c *Client) resolveBatchV5Raw(ctx context.Context, p *DeviceProfile, ids []string) ([]byte, error) {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = `"` + id + `"`
	}
	list := strings.Join(quoted, ",")
	// 与单集同一版式（明文、device_level 4、无 dr_scene），只是
	// mixed_video_id_map 的两个列表各装多集。
	body := []byte(fmt.Sprintf(`{"biz_param":{"detail_page_version":0,"device_level":4,"disable_digg_stat":false,"disable_video_relate_book":false,"need_all_video_definition":true,"need_mp4_align":false,"use_os_player":false,"use_server_dns":false,"video_platform":1024},"mixed_video_id_map":{"1004":[%s],"1001":[%s]}}`, list, list))
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
	return raw, nil
}

// parseBatchV5Playback 把批量响应拆成逐集播放模型；响应里缺失或解析
// 失败的集放进 missing，交由调用方回退单集解析。
func parseBatchV5Playback(raw []byte, ids []string) (map[string]*model.Playback, []string) {
	got := make(map[string]*model.Playback, len(ids))
	var missing []string
	for _, id := range ids {
		pb, _ := finishMobileV5Playback(raw, id)
		if pb != nil {
			got[id] = pb
		} else {
			missing = append(missing, id)
		}
	}
	return got, missing
}
