package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kaiye/meeting-room/internal/store"
	"github.com/kaiye/meeting-room/internal/summarize"
)

const titlePrompt = `你是会议命名助手。根据以下内容,输出一个简洁、具体的中文会议标题,概括会议核心议题,不超过 20 个字。只输出标题文字本身,不要引号、书名号、句号或任何解释。`

const titleFromContextPrompt = `你是会议命名助手。下面是会议的背景信息(如日历邀请、议程)。请从中提取会议标题并原样输出(可去掉日期、会议室、时间等修饰);如果没有明显的标题,再根据内容概括一个,不超过 20 个字。只输出标题文字本身,不要引号或任何解释。`

func meetingTitleCustomized(ctx context.Context, st *store.Store, id string) bool {
	m, err := st.GetMeeting(ctx, id)
	return err == nil && m.TitleCustomized
}

func cleanTitle(raw string) string {
	title := strings.TrimSpace(raw)
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = strings.TrimSpace(title[:i])
	}
	title = strings.Trim(title, `"'“”‘’《》。`)
	if r := []rune(title); len(r) > 40 {
		title = string(r[:40])
	}
	return title
}

// withDate suffixes the meeting's date. Recurring meetings ("Studio 周会")
// otherwise produce a history list of identical names. Uses the meeting's own
// date rather than today's, since summaries are often (re)generated later; and
// skips the suffix if the model already worked the date into the title.
func withDate(title string, at time.Time) string {
	date := at.Local().Format("2006-01-02")
	if strings.Contains(title, date) {
		return title
	}
	return title + " " + date
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// AutoTitle names the meeting from the best available source, in priority
// order: (1) the meeting's background info — its own title, extracted; (2)
// the completed summary; (3) a transcript excerpt. The meeting's date is
// appended to whatever the model returns. force is the explicit
// "AI rename" button: it overwrites even manually customized titles; the
// summarize-job path passes false.
func AutoTitle(ctx context.Context, st *store.Store, meetingID string, force bool) (string, bool, error) {
	ss, err := st.GetSummarySettings(ctx)
	if err != nil {
		return "", false, err
	}
	meeting, err := st.GetMeeting(ctx, meetingID)
	if err != nil {
		return "", false, err
	}

	pctx := ctx // debug records must survive the LLM call's own timeout
	record := func(source, sysPrompt, input, status, errMsg, title string, applied bool, elapsed time.Duration) {
		info := map[string]any{
			"kind": "autoname", "source": source, "model": ss.Model, "baseUrl": ss.BaseURL,
			"prompt": sysPrompt, "input": input,
			"elapsedMs": elapsed.Milliseconds(),
			"status":    status, "title": title, "applied": applied, "force": force,
		}
		if errMsg != "" {
			info["error"] = errMsg
		}
		if bts, jerr := json.Marshal(info); jerr == nil {
			_ = st.AddSummaryDebug(pctx, meetingID, string(bts))
		}
	}

	// Source 1: background info — LLM extracts the title from it (the
	// first line is often but not always the event name, so no heuristic).
	var source, sysPrompt, input string
	ctxText := strings.TrimSpace(meeting.Context)
	if ctxText != "" {
		source, sysPrompt, input = "context", titleFromContextPrompt, truncateRunes(ctxText, 4000)
	} else if sum, serr := st.GetSummary(ctx, meetingID); serr == nil && sum.Status == "completed" && strings.TrimSpace(sum.Content) != "" {
		// Source 2: the summary — densest, most accurate naming material.
		source, sysPrompt = "summary", titlePrompt
		input = "会议纪要:\n" + truncateRunes(sum.Content, 6000)
	} else {
		// Source 3: transcript excerpt.
		segs, err := st.GetSegments(ctx, meetingID)
		if err != nil {
			return "", false, err
		}
		if len(segs) == 0 {
			return "", false, fmt.Errorf("该会议还没有转录内容")
		}
		var b strings.Builder
		b.WriteString("会议转录节选:\n")
		for _, s := range segs {
			b.WriteString(s.Text)
			b.WriteByte('\n')
			if b.Len() > 6000 {
				break
			}
		}
		source, sysPrompt, input = "transcript", titlePrompt, b.String()
	}

	if ss.BaseURL == "" {
		return "", false, fmt.Errorf("总结 LLM 未配置:请在设置中填写服务地址")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	llmStart := time.Now()
	client := summarize.New(ss.BaseURL, ss.Model, ss.APIKey)
	raw, err := client.Chat(ctx, sysPrompt, input)
	if err != nil {
		record(source, sysPrompt, input, "failed", err.Error(), "", false, time.Since(llmStart))
		return "", false, err
	}
	title := cleanTitle(raw)
	if title == "" {
		record(source, sysPrompt, input, "failed", "LLM 未返回有效标题", "", false, time.Since(llmStart))
		return "", false, fmt.Errorf("LLM 未返回有效标题")
	}
	title = withDate(title, meeting.CreatedAt)
	applied, err := st.ApplyAutoTitle(pctx, meetingID, title, force)
	record(source, sysPrompt, input, "completed", "", title, applied, time.Since(llmStart))
	return title, applied, err
}
