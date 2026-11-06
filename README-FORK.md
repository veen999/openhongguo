# openhongguo fork 说明

本仓库是 [beilunyang/openhongguo](https://github.com/beilunyang/openhongguo)（以 v0.2.0 为基线）的自研 fork，为红果短剧 Android 客户端（`com.hongguo.app`）提供上游没有覆盖的能力：**评论/弹幕、第二播放通道、听书、小说正文**，以及若干目录字段的解析补全。

本文件只记录 fork 相对 upstream 的改动；未提及的方法（设备注册与签名、Discover/Search、详情/选集、榜单、筛选、演员、上新等）与 upstream 保持一致，不在此赘述。仓库根目录 `README.md` 是 fork 版介绍（顶部有 fork 说明与变更摘要，其余部分保留 upstream 原文与署名）。

## 改动总览

| 类别 | 位置 | 内容 |
| --- | --- | --- |
| 新增 | `sdk/comment.go` | `CommentList` 评论/弹幕接口 |
| 新增 | `sdk/reader.go`、`sdk/model/reader.go` | `AudioPlayback` / `ToneInfo` / `ChapterContent` 与 `ToneInfo` / `ChapterContent` 类型 |
| 新增 | `sdk/model/catalog.go` | `CommentPage` / `CommentItem` 等评论类型 |
| 修改 | `sdk/media.go` | `ResolvePlayback` 改为双通道：`video_tab` 优先、原 POST 通道兜底 |
| 修改 | `internal/protocol/app.go`、`internal/protocol/request.go` | 新增 `PathVideoTabModel` 路径与 `SignedVideoTabPairs` 签名查询构造 |
| 修改 | `internal/protocol/parse.go` | `enrichSummary` 等列表字段补全；`ParseCategoryLanding` 补章数/价格/关联有声书字段 |
| 修改 | `sdk/client.go`、`sdk/errors.go` | 正文内容键按设备缓存；新增 `ErrContentLocked` |
| 同步 | `bindings/mobile`（Kotlin 绑定） | 新增 `commentList` / `audioPlayback` / `toneInfo` / `chapterContent` 四个方法，错误码新增 `CONTENT_LOCKED` |

## 新增能力详解

### 1. 评论 / 弹幕：`CommentList`（sdk/comment.go）

upstream 没有评论接口。fork 接入官方 commentapi：

- `POST …/novel/commentapi/comment/list/{vid}/v1/`，`comment_type=20`、`group_type=30`，`book_id` 必须传真实 `series_id`（传错返回 103001）
- 数据流按 **30 秒时间窗**分页：一窗一调用，窗内以 `start_offset_time` + cursor 翻页；返回 `CommentPage`（含服务端报告的 `ReportedCount`）
- 同一数据流两种用法：按时间偏移渲染即**弹幕**，按点赞排序展示即**评论**；上游不下发昵称/头像，展示名约定为「剧友 + user_id 后四位」
- 复用 SDK 自身的游客设备注册与 `SignPOST` 签名，不需要额外身份材料
- 注意：匿名身份下是**采样返回**（实测某集报告 988 条、实际约取回 255 条去重结果），且采样池在同一时间窗内固定，多拉合并无法补全——调用方不要承诺全量

### 2. 第二播放通道：`video_tab`（sdk/media.go）

背景：自 2026-10-29 起 upstream 使用的 POST `/novel/player/video_model/v1/` 通道对匿名设备实际是「每设备一次」试看额度，用完即返回空模型（`ErrAnonymousLimited`），靠重注册设备续命会撞上游注册限流。

fork 逆向官方客户端发现第二条独立的发模通道，并改为默认路径：

- `GET /reading/bookapi/video_tab/video_model/v`，用已有游客身份 + `SignGET` 签名；查询 = 设备公共参数 + `video_id` + **`need_all_video_definition=true`**（缺此参数只回单个 720p 档；带上后回 360p–1080p 全档位，bytevc1、cenc-aes-ctr，含 kid/spade_a）
- 该通道配额与 POST 通道**相互独立**，POST 通道已被烧掉的设备也能立即经此通道取模
- `ResolvePlayback` 的行为：先 `resolvePlaybackViaTab`，任何失败回退 `resolvePlaybackViaPlayer`（upstream 原通道）；两边都空才上抛 `ErrAnonymousLimited` 触发设备轮换。响应信封与原通道一致，直接复用 `ParsePlayback`
- 未压测项：该通道长期/大批量使用的上限未知，调用方仍应保留节流

### 3. 听书：`AudioPlayback` / `ToneInfo`（sdk/reader.go）

- `AudioPlayback`：`GET /reading/reader/audio/playinfo/`（游客 + `SignGET`；`book_id` + `item_ids` + `tone_id=0` + `req_type=0`）。返回的音频模型与短剧视频模型**同构**（CENC 加密 M4A），内部包一层合成信封后复用 `ParsePlayback` + `ContentKey`，App 侧可与视频共用同一解密链
- **必须逐章单调**：一次传多章时上游会把 `item_id` 错标（返回的模型对不上请求的章节），fork 按单章调用约定实现，调用方不要试图批量
- `ToneInfo`：`GET /reading/bookapi/audio/toneinfo/`，返回音色列表与 `relate_novel_bookid`（非空 ⇔ 该书本身是有声书；也用于小说↔有声书互跳定位）

### 4. 小说正文：`ChapterContent`（sdk/reader.go）

两步，均对游客身份：

1. **注册内容键**：`POST /reading/crypt/registerkey`，body 的 `content` 为 `base64(iv16 || AES-128-CBC(seed))`，seed = `LE64(deviceId) || LE64(userId=0)`（必须小端；大端返回 500002 校验失败），使用内置固定 key/iv；响应 `data.key` 同法解出 32 位十六进制内容键
2. **取正文**：`GET /reading/reader/full/v?book_id&item_id&req_type=0`，密文 AES-CBC 解密 → gunzip → 章节 HTML（段落以 `<blk>` 组织）

fork 的封装行为：

- 内容键**按 deviceId 在 Client 内缓存**（`sdk/client.go` 的 `contentKeys`），一次注册覆盖该身份下所有书/章；解密失败时自动弃缓存、重注册、重试一次
- 未注册时上游正文是字面量 `"Invalid"`（crypt_status=2）；注册后仍取不到（付费/VIP 章节等情形）统一归为 `ErrContentLocked`，绑定层错误码 `CONTENT_LOCKED`，供 App 显示「本章需付费」锁定页。**fork 不提供任何付费绕过能力**

## 解析层补全（internal/protocol/parse.go）

upstream 的解析会丢弃一批上游实际下发的字段，fork 补回：

- 列表项（Discover/Search/落地页等）：`enrichSummary` 配合 `catalogVideo` 解包，补齐**热度、评分、标签、播放量、平台**等字段到 `SeriesSummary`（此前筛选页的评分/热度曾因解析丢失而恒空）
- `ParseCategoryLanding`：补映射 `chapter_number`（→ 章数）、`total_price`（整本付费价格）、`related_audio_bookids`（关联有声书 id；注意上游是 `"[id,id]"` **字符串**形态而非 JSON 数组）
- 顺带修正：书籍条目的章数在 `episodeCount`、简介在 `description` 字段上，与短剧共用 `SeriesSummary` 结构

## 绑定层

`bindings/mobile` 的 Kotlin 绑定与 Go 层同步生成/补齐，App 侧（`dev.openhongguo.*`）可见：`commentList(episodeId, seriesId, options)`、`audioPlayback(bookId, itemId, toneId)`、`toneInfo(bookId)`、`chapterContent(bookId, itemId)`；`resolvePlayback` 签名不变（双通道在 Go 层内部完成）。

## 构建 AAR

fork 根目录提供打包脚本（`build-aar.sh` / `build-aar2.sh`）：先由 `bindings/mobile/gen` 生成绑定，再用 gomobile/gobind 构建 Android AAR（四 ABI 的 `libgojni.so`），最终产物为 `openhongguo-release.aar`（约 12.6 MB），供 App 以本地 AAR 方式依赖（`app/libs/openhongguo-release.aar`）。工具链：Go（见 `go.mod`）、gomobile/gobind、JDK 17、Android SDK/NDK。

## 与 upstream 的关系

- 本 fork 的改动均为**加法**：不删除、不改名 upstream 的任何公开方法；upstream 后续版本若合入相同能力，可逐项比对替换
- 接口均来自对官方客户端的独立逆向分析；匿名数据完整性受上游策略影响（见上文采样与限流说明）
