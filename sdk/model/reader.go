package model

// ChapterContent 是一章小说的正文，已经解密并解压。
// HTML 是服务端给的章节 HTML（<article> 包裹，段落是 <blk> 元素），
// Paragraphs 是从中按顺序抽出的纯文本段落，供不想解析 HTML 的调用方直接排版。
type ChapterContent struct {
	Title      string   `json:"title"`
	HTML       string   `json:"html"`
	Paragraphs []string `json:"paragraphs,omitempty"`
}

// ToneInfo 是有声书的音色信息：这本书是否有可播的音色，
// 以及（当本书本身是有声书时）它对应的小说 book_id。
type ToneInfo struct {
	RelateNovelID string `json:"relate_novel_id,omitempty"`
	HasTones      bool   `json:"has_tones"`
	// HasAudioTones 表示本书有真人主播录制音色（audio_tones 非空）：
	// 这类书 playinfo 用 tone_id=0 即主播原声。
	HasAudioTones bool `json:"has_audio_tones"`
	// RecommendTone 是服务端推荐的 TTS 音色 id（recommend_tone）；
	// 纯 TTS 书没有 0 号音色，playinfo 必须传真实音色 id。
	RecommendTone int `json:"recommend_tone,omitempty"`
	// TTSToneIDs 是本书可用的 TTS 音色 id 列表（tts_tones）。
	TTSToneIDs []int `json:"tts_tone_ids,omitempty"`
	// Tones 是完整的音色表（真人主播在前、TTS 在后），带名称，供
	// 调用方做音色选择。注意音色表要查「小说 id」的那份：小说的真人
	// 主播是独立的有声书（ABookID），TTS 音色挂在小说 id 下；有声书
	// 自身的音色表里 audio_tones 只含它自己。
	Tones []ToneEntry `json:"tones,omitempty"`
}

// RelatedVideo 是小说关联的一部改编短剧（related_video 的
// related_video_version_list 一条）：VideoVersionID 是短剧侧的
// series_id，可直接进短剧详情；SubTitle 是副标题行（如集数/
// 播放量文案），服务端给出时才有。
type RelatedVideo struct {
	VideoVersionID string `json:"video_version_id"`
	Title          string `json:"title"`
	Cover          string `json:"cover,omitempty"`
	SubTitle       string `json:"sub_title,omitempty"`
}

// ToneEntry 是音色表里的一条可选音色。
type ToneEntry struct {
	// ID 是 TTS 音色 id（>0）；真人主播条目为 0。
	ID int `json:"id"`
	// Title 是展示名：TTS 如「成熟大叔音升级版」，真人如「主播：海风飨风林」。
	Title string `json:"title"`
	// Description 是 TTS 音色的风格短描述（如「超自然」），真人条目为空。
	Description string `json:"description,omitempty"`
	// Badge 是 TTS 音色的角标（如「多角色」），无角标时为空。
	Badge string `json:"badge,omitempty"`
	// Human 表示真人主播条目：收听它要切到 ABookID 那本有声书以
	// tone_id=0 播放（章节按序号与小说对齐）。TTS 条目为 false，
	// 直接在小说 id 上以 tone_id=ID 播放。
	Human bool `json:"human"`
	// ABookID 是真人主播的有声书 id；TTS 条目为空。
	ABookID string `json:"abook_id,omitempty"`
}
