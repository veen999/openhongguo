package sdk

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/beilunyang/openhongguo/sdk/model"
)

// probeEpisodeID 是健康探测专用的探针集，只用来问「通道现在发不发放」，
// 取到的模型直接丢弃。用长期在架的热门剧集，避免探针集下架让全部通道
// 被误判为不发放（真取址时各通道仍会按健康计划与全 down 重探兜底）。
const probeEpisodeID = "7699003626647194649"

// ProbePlaybackChannels 并发探测全部本地取址通道（应用启动时调用）：
// 每条通道用探针集各试一次，结果落进通道健康表——发放的通道记成功
// 并成为首选，空发放的进 45 秒冷却。之后正常取址直接用活通道，不再
// 每次从链首逐个撞死通道；通道恢复后，冷却到期或下一次启动探测会
// 重新发现它。探测不走 withFreshDevice 的身份轮换，不触碰计算方式
// 策略状态（本地失败计数、自动切换、恢复探测都照旧）。返回各通道
// 本次是否发放。
func (c *Client) ProbePlaybackChannels(ctx context.Context) map[string]bool {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	results := make(map[string]bool, 7)
	var mu sync.Mutex
	var wg sync.WaitGroup
	run := func(name string, attempt func() (*model.Playback, error)) {
		defer wg.Done()
		pb, err := attempt()
		c.noteChannelOutcome(ctx, name, pb, err)
		mu.Lock()
		results[name] = pb != nil
		mu.Unlock()
	}
	if p, err := c.playbackIdentity(ctx); err == nil {
		wg.Add(5)
		go run(channelV5, func() (*model.Playback, error) {
			return c.resolvePlaybackViaMobileV5(ctx, p, probeEpisodeID)
		})
		go run(channelTabMulti, func() (*model.Playback, error) {
			return c.resolvePlaybackViaTabMulti(ctx, p, probeEpisodeID)
		})
		go run(channelMultiV2, func() (*model.Playback, error) {
			return c.resolvePlaybackViaMultiPlayerV2(ctx, p, probeEpisodeID)
		})
		go run(channelMulti, func() (*model.Playback, error) {
			return c.resolvePlaybackViaMultiPlayer(ctx, p, probeEpisodeID)
		})
		go run(channelV5Batch, func() (*model.Playback, error) {
			raw, err := c.resolveBatchV5Raw(ctx, p, []string{probeEpisodeID})
			if err != nil {
				return nil, err
			}
			pb, _ := finishMobileV5Playback(raw, probeEpisodeID)
			return pb, nil
		})
	}
	wg.Add(2)
	go run(channelPlayer, func() (*model.Playback, error) {
		return c.resolvePlaybackViaPlayer(ctx, probeEpisodeID)
	})
	go run(channelTab, func() (*model.Playback, error) {
		return c.resolvePlaybackViaTab(ctx, probeEpisodeID)
	})
	wg.Wait()
	if c.logger != nil {
		names := make([]string, 0, len(results))
		for n := range results {
			names = append(names, n)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, n := range names {
			state := "down"
			if results[n] {
				state = "ok"
			}
			parts = append(parts, n+"="+state)
		}
		c.logger.Printf("playback channel probe: %s", strings.Join(parts, " "))
	}
	// 齐射全灭不等于本地全灭（新身份试用期内必全灭），但也不能就此
	// 算数：后台再跑一遍完整复核，确认全灭才给计算方式打上预切标
	// 记。复核与恢复探测单飞共用，叠跑时自动跳过。
	allDown := len(results) > 0
	for _, alive := range results {
		if alive {
			allDown = false
			break
		}
	}
	if allDown && !inLocalConfirm(ctx) {
		c.confirmLocalOrFlipAsync()
	}
	return results
}
