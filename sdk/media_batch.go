package sdk

import (
	"context"
	"fmt"
	"strings"

	"github.com/beilunyang/openhongguo/internal/protocol"
	"github.com/beilunyang/openhongguo/sdk/model"
	"github.com/beilunyang/openhongguo/internal/protocol/sign"
)

// batchV5ChunkSize 是一次移动端 v5 批量请求最多携带的集数。响应按每集
// 约 19KB 估算，20 集约 380KB，仍在单次响应可接受的范围内。
const batchV5ChunkSize = 20

// ResolvePlaybackBatch 批量解析多集的播放模型。优先走移动端 v5 通道
// 一次请求多集（同一播放身份复用，不逐集注册）；批量响应未覆盖的集
// 回退到单集 ResolvePlayback 的完整通道链。返回值是成功解析的播放
// 模型与逐集错误：整体失败时，请求过的每一集都会出现在 errs 里。
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
	p, idErr := c.playbackIdentity(ctx)
	for start := 0; start < len(uniq); start += batchV5ChunkSize {
		end := start + batchV5ChunkSize
		if end > len(uniq) {
			end = len(uniq)
		}
		chunk := uniq[start:end]
		var missing []string
		if idErr != nil {
			missing = chunk
		} else if raw, err := c.resolveBatchV5Raw(ctx, p, chunk); err != nil {
			missing = chunk
		} else {
			var got map[string]*model.Playback
			got, missing = parseBatchV5Playback(raw, chunk)
			for id, pb := range got {
				playbacks[id] = pb
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
	body := []byte(fmt.Sprintf(`{"biz_param":{"detail_page_version":0,"device_level":1,"disable_digg_stat":false,"disable_video_relate_book":false,"need_all_video_definition":true,"need_mp4_align":false,"use_os_player":false,"use_server_dns":false,"video_platform":1024},"mixed_video_id_map":{"1004":[%s],"1001":[%s]},"dr_scene":"player"}`, list, list))
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
