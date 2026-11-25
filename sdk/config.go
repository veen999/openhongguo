package sdk

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/beilunyang/openhongguo/internal/protocol"
	"github.com/beilunyang/openhongguo/internal/protocol/sign"
)

// 上游配置驱动（2026-11-25）：官方客户端的行为开关不在代码里写死，
// 而是启动时从平台 AB 配置面拉取（SsConfigMgr.getABValueJson 读
// ab_result 同名键，缺省回退 native settings）。本文件复刻这条读取
// 路径中与播放/目录相关的部分：video_tab 总开关、播放路径实验
// video_player_api_dr_scene、continue_watch 分组与个人化开关。
// 读到的 dr_scene 开关直接决定 multi V2 通道的场景段（见
// resolvePlaybackViaMultiPlayerV2），其余键落进快照供界面展示与
// 变更日志——平台改配置，我方下一次启动即跟随，不再靠人工探针
// 发现。诚实边界：静默规则变化（端点下线、常量轮换）不带配置信
// 号，配置面跟随不了那一类，只能靠通道健康探测缩短发现时间。

// ConfigSnapshot 是一次上游配置拉取的结果，供「我的」页展示
// （B2：计算方式副文案追加一行）与变更比对。
type ConfigSnapshot struct {
	// 拉取时刻（Unix 毫秒）。
	FetchedAt int64 `json:"fetchedAt"`
	// 是否至少拉到了 ab_result（其余端点失败不影响已得键）。
	OK bool `json:"ok"`
	// video_tab 总开关（ab_result.video_tab；键缺失记 false）。
	VideoTab bool `json:"videoTab"`
	// 播放路径实验 video_player_api_dr_scene 的 enable 值；键缺
	// 失 = 未命中实验 = false（官方读取点语义：默认 {"enable":false}）。
	DrSceneEnabled bool `json:"drSceneEnabled"`
	// 当前生效的播放路径标签："V2 · dr_scene" 或 "V1"。
	PlaybackPath string `json:"playbackPath"`
	// continue_watch_ab 的 ab_type；未拉到时为 -1。
	ContinueWatchType int `json:"continueWatchType"`
	// 个人化开关（switch/info.personal_switch）。
	PersonalSwitch bool `json:"personalSwitch"`
}

// FetchConfigSnapshot 拉取上游 AB 配置并更新客户端内的生效快照
// （dr_scene 随即作用于后续取址）。单个端点失败不让整次拉取失
// 败：拿到的键照常生效，ab_result 也失败时返回错误且不更新快
// 照。每次拉取与上一份快照比对，关键键变化写日志。
func (c *Client) FetchConfigSnapshot(ctx context.Context) (*ConfigSnapshot, error) {
	return withFreshDevice(ctx, c, false, func(ctx context.Context) (*ConfigSnapshot, error) {
		p, err := c.ensureSearchGuest(ctx)
		if err != nil {
			return nil, err
		}
		snap := &ConfigSnapshot{FetchedAt: c.clock().UnixMilli(), ContinueWatchType: -1}
		get := func(path string) (map[string]json.RawMessage, error) {
			ticket := c.clock().UnixMilli()
			rawQuery, headers := sign.SignGET(protocol.DeviceQueryPairs(c.identity(p)), ticket, p.DeviceID, protocol.VersionName, nil)
			raw, _, err := c.doRaw(ctx, "GET", c.apiOrigin+path+"?"+rawQuery, anonymousHeaders(c, p, ticket, headers), nil)
			if err != nil {
				return nil, err
			}
			var env struct {
				Code int                        `json:"code"`
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				return nil, err
			}
			if env.Code != 0 {
				return nil, fmt.Errorf("config %s: code %d", path, env.Code)
			}
			return env.Data, nil
		}
		ab, abErr := get(protocol.PathABResult)
		if abErr == nil {
			snap.OK = true
			if raw, ok := ab["video_tab"]; ok {
				_ = json.Unmarshal(raw, &snap.VideoTab)
			}
			snap.DrSceneEnabled = parseDrScene(ab)
		}
		if !snap.DrSceneEnabled {
			// ugc AB 配置面同样可能下发该键（默认组之外），扫一遍。
			if ugc, err := get(protocol.PathUGCABConfig); err == nil {
				snap.DrSceneEnabled = parseDrScene(ugc)
			}
		}
		if cw, err := get(protocol.PathContinueWatchAB); err == nil {
			if raw, ok := cw["ab_type"]; ok {
				_ = json.Unmarshal(raw, &snap.ContinueWatchType)
			}
		}
		if sw, err := get(protocol.PathSwitchInfo); err == nil {
			if raw, ok := sw["personal_switch"]; ok {
				_ = json.Unmarshal(raw, &snap.PersonalSwitch)
			}
		}
		if !snap.OK {
			return nil, fmt.Errorf("config snapshot: ab_result unavailable: %w", abErr)
		}
		if snap.DrSceneEnabled {
			snap.PlaybackPath = "V2 · dr_scene"
		} else {
			snap.PlaybackPath = "V1"
		}
		c.configMu.Lock()
		prev := c.configSnap
		c.configSnap = snap
		c.configMu.Unlock()
		if prev != nil && c.logger != nil {
			if prev.VideoTab != snap.VideoTab {
				c.logger.Printf("upstream config changed: video_tab %v -> %v", prev.VideoTab, snap.VideoTab)
			}
			if prev.DrSceneEnabled != snap.DrSceneEnabled {
				c.logger.Printf("upstream config changed: dr_scene %v -> %v (playback path %s)", prev.DrSceneEnabled, snap.DrSceneEnabled, snap.PlaybackPath)
			}
			if prev.ContinueWatchType != snap.ContinueWatchType {
				c.logger.Printf("upstream config changed: continue_watch ab_type %d -> %d", prev.ContinueWatchType, snap.ContinueWatchType)
			}
		}
		return snap, nil
	})
}

// parseDrScene 从一份 AB 数据里读 video_player_api_dr_scene 的
// enable 值：值可能是对象 {"enable":true}，也可能是同内容的
// JSON 字符串（官方 getABValueJson 两形都收）。键缺失返回
// false（未命中实验，官方默认即 false）。
func parseDrScene(data map[string]json.RawMessage) bool {
	raw, ok := data["video_player_api_dr_scene"]
	if !ok {
		return false
	}
	var obj struct {
		Enable bool `json:"enable"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Enable
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil && str != "" {
		if err := json.Unmarshal([]byte(str), &obj); err == nil {
			return obj.Enable
		}
	}
	return false
}

// drSceneEnabled 报告当前生效快照是否命中 dr_scene 实验；从未
// 拉取过配置时为 false（与官方默认一致，行为不变）。
func (c *Client) drSceneEnabled() bool {
	c.configMu.Lock()
	defer c.configMu.Unlock()
	return c.configSnap != nil && c.configSnap.DrSceneEnabled
}

// configSnapshot 返回当前生效快照（可能为 nil）。
func (c *Client) configSnapshot() *ConfigSnapshot {
	c.configMu.Lock()
	defer c.configMu.Unlock()
	return c.configSnap
}
