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
}
