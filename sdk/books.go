package sdk

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/beilunyang/openhongguo/internal/protocol"
	"github.com/beilunyang/openhongguo/internal/protocol/sign"
	"github.com/beilunyang/openhongguo/sdk/model"
)

// Categories 加载书籍分类的标签页：全部、男生、女生和默认标签页。
// 线上服务在这里不返回行（2026-10-24 实测）；列表及其筛选来自 CategoryLanding。
//
// 路径和请求类取自 APK：GetNewCategoryFrontPageRequest 发往
// /reading/bookapi/new_category/front/v:version/。
func (c *Client) Categories(ctx context.Context, categoryID string) (*model.CategoryPage, error) {
	return withFreshDevice(ctx, c, false, func(ctx context.Context) (*model.CategoryPage, error) {
		if strings.TrimSpace(categoryID) == "" {
			return nil, fmt.Errorf("missing category ID")
		}
		const offset = 0
		p, err := c.ensureSearchGuest(ctx)
		if err != nil {
			return nil, err
		}
		ticket := c.clock().UnixMilli()
		rawQuery, headers := sign.SignGET(protocol.SignedCategoryPairs(c.identity(p), categoryID, offset, 20), ticket, p.DeviceID, protocol.VersionName, nil)
		raw, _, err := c.doRaw(ctx, "GET", c.apiOrigin+protocol.PathCategory+"?"+rawQuery, anonymousHeaders(c, p, ticket, headers), nil)
		if err != nil {
			return nil, err
		}
		return protocol.ParseCategoryFront(raw)
	})
}

// LandingOptions 选择一个分类落地页。
//
// Tab 是 Categories 给出的 tab 值：8（全部，默认）、1（男生）或 0（女生）。
// Filters 把 CategoryPage.Filters 中的筛选组键（book_status、sort_by、
// sub_category、word_type）映射到该组的某个选项值。
// Cursor 是上一页的 NextOffset。
type LandingOptions struct {
	Tab     string
	Filters map[string]string
	Cursor  string
}

// CategoryLanding 加载书籍分类下列表的一页，以及它的筛选面板。
// 这些行是书（小说），不是短剧。
func (c *Client) CategoryLanding(ctx context.Context, categoryID string, opts LandingOptions) (*model.CategoryPage, error) {
	return withFreshDevice(ctx, c, false, func(ctx context.Context) (*model.CategoryPage, error) {
		if strings.TrimSpace(categoryID) == "" {
			return nil, fmt.Errorf("missing category ID")
		}
		gender := ""
		switch opts.Tab {
		case "", "8":
		case "1", "0":
			// tab 以 query_gender 发送；tab_type 不起作用（实测）。
			gender = opts.Tab
		default:
			return nil, fmt.Errorf("unknown category tab %q; use 8, 1, or 0", opts.Tab)
		}
		for key := range opts.Filters {
			if !slices.ContainsFunc(protocol.LandingFilterParams, func(kv [2]string) bool { return kv[0] == key }) {
				return nil, fmt.Errorf("unknown category filter %q", key)
			}
		}
		offset, err := protocol.SearchOffset(opts.Cursor)
		if err != nil {
			return nil, err
		}
		p, err := c.ensureSearchGuest(ctx)
		if err != nil {
			return nil, err
		}
		ticket := c.clock().UnixMilli()
		rawQuery, headers := sign.SignGET(protocol.SignedCategoryLandingPairs(c.identity(p), categoryID, gender, opts.Filters, offset, 20), ticket, p.DeviceID, protocol.VersionName, nil)
		raw, _, err := c.doRaw(ctx, "GET", c.apiOrigin+protocol.PathCategoryLanding+"?"+rawQuery, anonymousHeaders(c, p, ticket, headers), nil)
		if err != nil {
			return nil, err
		}
		return protocol.ParseCategoryLanding(raw)
	})
}

// RelatedVideo 加载一部小说关联的改编短剧列表（小说→短剧方
// 向）：GET /reading/reader/related_video/v1/，参数只有 book_id。
// 2026-11-25 探针定案：匿名下该端点对有确证改编的小说也恒返回
// 空 data（两台身份 × v1/v2 × 双 host × 附加参数全形态皆空，
// book_id 是唯一正确参数名——novel_book_id 会被 PARAM_INVALID
// 拒绝）；小说自身的详情响应也不带任何改编字段。产品路径走
// RelatedDramas 的反查兜底，本方法保留作端点直读（端点将来
// 放数据时优先命中）。列表为空是正常结果而非错误。
func (c *Client) RelatedVideo(ctx context.Context, bookID string) ([]model.RelatedVideo, error) {
	return withFreshDevice(ctx, c, false, func(ctx context.Context) ([]model.RelatedVideo, error) {
		if !validID(bookID) {
			return nil, fmt.Errorf("invalid book ID %q", bookID)
		}
		p, err := c.ensureSearchGuest(ctx)
		if err != nil {
			return nil, err
		}
		pairs := append(protocol.DeviceQueryPairs(c.identity(p)), [2]string{"book_id", bookID})
		ticket := c.clock().UnixMilli()
		rawQuery, headers := sign.SignGET(pairs, ticket, p.DeviceID, protocol.VersionName, nil)
		raw, _, err := c.doRaw(ctx, "GET", c.apiOrigin+protocol.PathRelatedVideo+"?"+rawQuery, anonymousHeaders(c, p, ticket, headers), nil)
		if err != nil {
			return nil, err
		}
		return protocol.ParseRelatedVideo(raw)
	})
}

// RelatedDramas 返回一部小说的改编短剧（小说页「相关短剧」区
// 的数据）。先直读 related_video 端点；端点匿名恒空（见
// RelatedVideo 注释），故兜底走反查：短剧详情匿名可读且带
// related_novel_book_id（短剧→小说方向是通的），于是以小说
// 标题搜索短剧、对候选批量取详情、只保留
// related_novel_book_id 与本书一致的——核对是精确等值，不是
// 标题模糊匹配。2026-11-25 双例实证：《仙界闭关小能手》→
// 《聚宝仙盆之杂灵根才是真BOSS》季条目命中，同页其他候选被
// 正确滤除；《开局迎娶亡国女帝，系统激活！》→《乱世边军：
// 儒骨人屠》命中。同一合集（多季）只留搜索序最靠前的一条。
// 无改编时返回空列表，调用方据此整区不显示。
func (c *Client) RelatedDramas(ctx context.Context, bookID string) ([]model.RelatedVideo, error) {
	if direct, err := c.RelatedVideo(ctx, bookID); err == nil && len(direct) > 0 {
		return direct, nil
	}
	book, err := c.GetSeries(ctx, bookID)
	if err != nil {
		return nil, err
	}
	if book.Title == "" {
		return nil, nil
	}
	page, err := c.Search(ctx, book.Title, SearchOptions{})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(page.Items))
	for _, it := range page.Items {
		ids = append(ids, it.ID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	details, err := c.GetSeriesBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]model.RelatedVideo, 0, len(details))
	seenAlbum := map[string]bool{}
	for _, d := range details {
		s := d.Series
		if s.RelatedNovelBookID != bookID {
			continue
		}
		if s.RelatedAlbumID != "" {
			if seenAlbum[s.RelatedAlbumID] {
				continue
			}
			seenAlbum[s.RelatedAlbumID] = true
		}
		sub := s.HeatText
		if s.EpisodeCount > 0 {
			sub = fmt.Sprintf("共%d集", s.EpisodeCount)
		}
		out = append(out, model.RelatedVideo{
			VideoVersionID: s.ID,
			Title:          s.Title,
			Cover:          s.CoverURL,
			SubTitle:       sub,
		})
	}
	return out, nil
}
