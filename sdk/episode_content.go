package sdk

import "github.com/beilunyang/openhongguo/sdk/model"

// noteEpisodeContentTypes 登记一批分集的 content_type（详情/选集解析
// 后调用）。取址时据此组 video_model 请求体：上游对 PUGC(1001) 等
// 类型要求请求 content_type 与单集真实类型一致，写死 1 会被回
// 101000（2026-11-26 实测）。ContentUnknown(0) 不登记，保留默认。
func (c *Client) noteEpisodeContentTypes(episodes []model.Episode) {
	if len(episodes) == 0 {
		return
	}
	c.epContentMu.Lock()
	defer c.epContentMu.Unlock()
	if c.epContentTypes == nil {
		c.epContentTypes = map[string]model.ContentType{}
	}
	for _, ep := range episodes {
		if ep.ID != "" && ep.ContentType != model.ContentUnknown {
			c.epContentTypes[ep.ID] = ep.ContentType
		}
	}
}

// noteEpisodeContentType 登记单集的 content_type（一集兜底重试命中后
// 回写，后续同集直接用对的值）。
func (c *Client) noteEpisodeContentType(episodeID string, ct model.ContentType) {
	if episodeID == "" || ct == model.ContentUnknown {
		return
	}
	c.epContentMu.Lock()
	defer c.epContentMu.Unlock()
	if c.epContentTypes == nil {
		c.epContentTypes = map[string]model.ContentType{}
	}
	c.epContentTypes[episodeID] = ct
}

// episodeContentType 返回登记过的分集 content_type；未登记返回
// ContentUnknown(0)，调用方回落默认 1。
func (c *Client) episodeContentType(episodeID string) model.ContentType {
	c.epContentMu.Lock()
	defer c.epContentMu.Unlock()
	return c.epContentTypes[episodeID]
}
