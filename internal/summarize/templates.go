package summarize

import "github.com/kaiye/meeting-room/internal/store"

// Built-in template seeds. They live in the DB after first startup and are
// fully user-editable in settings; this list only fills missing rows.

const promptCommon = `你是专业的会议纪要助手。用户会提供一份带时间戳和说话人标签(A/B/C... 或真名)的会议转录。
要求:使用与转录内容相同的语言输出;忠于原文,不编造未提及的内容;转录中的口误、重复和口水词应被忽略;输出使用 Markdown 格式。`

func SeedTemplates() []store.SummaryTemplate {
	return []store.SummaryTemplate{
		{
			ID:          "standard",
			Name:        "标准会议纪要",
			Description: "关键要点、讨论内容、决策与行动项的完整纪要",
			Prompt: promptCommon + `
请生成一份标准会议纪要,包含以下部分:
## 会议摘要 (2-3 句概括)
## 关键要点 (分条列出)
## 讨论详情 (按话题分小节)
## 决策 (明确达成的结论)
## 行动项 (格式:事项 — 负责人(如可识别) — 期限(如提及))`,
		},
		{
			ID:          "action-items",
			Name:        "行动项清单",
			Description: "只提取待办、负责人和截止时间",
			Prompt: promptCommon + `
只提取会议中的行动项/待办事项,输出一个 Markdown 任务列表:
- [ ] 事项描述 — 负责人(说话人标签或名字,如可识别) — 期限(如提及)
若无明确行动项,说明"本次会议未产生明确行动项"并列出隐含的跟进事项。`,
		},
		{
			ID:          "decisions",
			Name:        "决策记录",
			Description: "达成的结论、备选方案与理由",
			Prompt: promptCommon + `
提取会议中做出的所有决策,每个决策一节:
### 决策:<一句话结论>
- 背景:为什么讨论这个问题
- 备选方案:讨论过但未采纳的选项(如有)
- 理由:选择该方案的原因
- 相关发言人:说话人标签`,
		},
		{
			ID:          "qa",
			Name:        "问答摘要",
			Description: "以问题-回答对的形式整理讨论",
			Prompt: promptCommon + `
将会议内容整理为问答形式:提取讨论中被提出的问题(显式或隐式),以及会上给出的回答/结论。
格式:
**Q: 问题**
A: 回答要点(注明说话人标签)
未被回答的问题单独列在最后的"待解决问题"一节。`,
		},
	}
}
