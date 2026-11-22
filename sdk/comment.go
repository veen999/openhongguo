package sdk

import (
	"context"
	"fmt"
	"strings"

	"github.com/beilunyang/openhongguo/internal/protocol"
	"github.com/beilunyang/openhongguo/internal/protocol/sign"
	"github.com/beilunyang/openhongguo/sdk/model"
)

// CommentOptions 选择 comment/list 的一个时间窗口。StartOffsetMs 是窗口
// 在分集时间轴上的起始毫秒（首窗为 0，之后用上一窗的 NextStartMs）；
// Cursor 是上一窗给出的游标（首窗为空）；DurationMs 是分集总时长毫秒。
type CommentOptions struct {
	StartOffsetMs int64
	Cursor        string
	DurationMs    int64
}

// CommentList 加载一集的评论与弹幕，一次一个时间窗口。评论和弹幕是同一
// 条流（commentapi 的 comment_type 20）：按 OffsetMs 渲染即弹幕形态，
// 按 DiggCount 降序即评论形态。episodeID 是分集 id，seriesID 是它所属
// 短剧的 id，两者都必须是真实 id：seriesID 为空或 "0" 时服务端会以
// 参数错误（103001）拒绝，这里先在本地报错。翻页由调用方把返回的
// NextStartMs 和 Cursor 填回 CommentOptions 继续调用；HasMore 为假或
// 某窗没有条目时即到底。匿名身份下发的条数可能少于 ReportedCount。
func (c *Client) CommentList(ctx context.Context, episodeID, seriesID string, opts CommentOptions) (*model.CommentPage, error) {
	return withFreshDevice(ctx, c, false, func(ctx context.Context) (*model.CommentPage, error) {
		if !validID(episodeID) {
			return nil, fmt.Errorf("invalid episode ID")
		}
		if strings.TrimSpace(seriesID) == "" || strings.TrimSpace(seriesID) == "0" || !validID(seriesID) {
			return nil, fmt.Errorf("invalid series ID: comment list needs the real series ID as book_id")
		}
		if opts.StartOffsetMs < 0 {
			return nil, fmt.Errorf("invalid start offset")
		}
		if opts.DurationMs < 0 {
			return nil, fmt.Errorf("invalid duration")
		}
		body, err := protocol.CommentListBody(episodeID, seriesID, opts.StartOffsetMs, opts.DurationMs, opts.Cursor)
		if err != nil {
			return nil, err
		}
		p, err := c.ensureSearchGuest(ctx)
		if err != nil {
			return nil, err
		}
		path := fmt.Sprintf(protocol.PathCommentList, episodeID)
		ticket := c.clock().UnixMilli()
		rawQuery, headers := sign.SignPOST(path, protocol.DeviceQueryPairs(c.identity(p)), ticket, p.DeviceID, protocol.VersionName, body, nil)
		raw, _, err := c.doRaw(ctx, "POST", c.apiOrigin+path+"?"+rawQuery, anonymousHeaders(c, p, ticket, headers), body)
		if err != nil {
			return nil, err
		}
		res, err := protocol.ParseCommentList(raw, opts.StartOffsetMs)
		if err != nil {
			return nil, err
		}
		return &model.CommentPage{
			Items:         res.Items,
			Cursor:        res.Cursor,
			NextStartMs:   res.NextStartMs,
			HasMore:       res.HasMore,
			ReportedCount: res.ReportedCount,
		}, nil
	})
}

// PanelCommentOptions 是评论面板流的一页请求。Cursor 是上一页给出的
// 游标（首页为空）；Count 是每页条数（<=0 时用官方默认 10）。
type PanelCommentOptions struct {
	Cursor string
	Count  int
}

// CommentPanelList 加载一集的官方评论面板流：与 CommentList 是同一条
// comment/list 路由的两种请求形状——CommentList 是弹幕形状（服务端采样、
// 条目不带昵称头像），本方法是官方 App 评论面板的形状（comment_source=4
// NovelItemComment、server_channel=18 NovelItemList、comment_type=4
// NewItem、sort=1 SmartHot，business_param 带 need_count 与
// req_type=video_innerflow；参数形状与枚举值 2026-11-22 反编译官方
// 7.4.5.16 得出）。返回的条目带昵称、头像、发布时间与回复数。翻页由
// 调用方把返回的 Cursor 填回 PanelCommentOptions 继续调用，HasMore
// 为假即到底。
func (c *Client) CommentPanelList(ctx context.Context, episodeID, seriesID string, opts PanelCommentOptions) (*model.CommentPanelPage, error) {
	return withFreshDevice(ctx, c, false, func(ctx context.Context) (*model.CommentPanelPage, error) {
		if !validID(episodeID) {
			return nil, fmt.Errorf("invalid episode ID")
		}
		if strings.TrimSpace(seriesID) == "" || strings.TrimSpace(seriesID) == "0" || !validID(seriesID) {
			return nil, fmt.Errorf("invalid series ID: panel comment list needs the real series ID as book_id")
		}
		body, err := protocol.PanelCommentListBody(episodeID, seriesID, opts.Cursor, opts.Count)
		if err != nil {
			return nil, err
		}
		p, err := c.ensureSearchGuest(ctx)
		if err != nil {
			return nil, err
		}
		path := fmt.Sprintf(protocol.PathCommentList, episodeID)
		ticket := c.clock().UnixMilli()
		rawQuery, headers := sign.SignPOST(path, protocol.DeviceQueryPairs(c.identity(p)), ticket, p.DeviceID, protocol.VersionName, body, nil)
		raw, _, err := c.doRaw(ctx, "POST", c.apiOrigin+path+"?"+rawQuery, anonymousHeaders(c, p, ticket, headers), body)
		if err != nil {
			return nil, err
		}
		res, err := protocol.ParsePanelCommentList(raw)
		if err != nil {
			return nil, err
		}
		return &model.CommentPanelPage{
			Items:   res.Items,
			Cursor:  res.Cursor,
			HasMore: res.HasMore,
			Total:   res.Total,
		}, nil
	})
}
