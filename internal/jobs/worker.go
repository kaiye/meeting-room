// Package jobs runs the background transcription worker. It is the Go
// equivalent of the desktop app's import/retranscription orchestration:
// claim a job from the local queue, normalize audio with ffmpeg, submit large
// serial chunks to the transcription service, persist segments, and push
// progress over the WebSocket hub.
package jobs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/kaiye/meeting-room/internal/audio"
	"github.com/kaiye/meeting-room/internal/config"
	"github.com/kaiye/meeting-room/internal/store"
	"github.com/kaiye/meeting-room/internal/summarize"
	"github.com/kaiye/meeting-room/internal/transcribe"
	"github.com/kaiye/meeting-room/internal/ws"
)

type Worker struct {
	cfg config.Config
	st  *store.Store
	hub *ws.Hub
}

func NewWorker(cfg config.Config, st *store.Store, hub *ws.Hub) *Worker {
	return &Worker{cfg: cfg, st: st, hub: hub}
}

// Run polls for queued jobs until ctx is cancelled. One job at a time:
// the GPU service holds a single inference lock anyway, so server-side
// concurrency would only build a queue on the other end.
func (w *Worker) Run(ctx context.Context) {
	if n, err := w.st.RequeueOrphanedJobs(ctx); err != nil {
		log.Printf("worker: requeue orphaned jobs: %v", err)
	} else if n > 0 {
		log.Printf("worker: requeued %d orphaned job(s) from previous run", n)
	}
	log.Printf("job worker started (chunk=%ds)", w.cfg.ChunkSeconds)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		job, err := w.st.ClaimNextJob(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("worker: claim job: %v", err)
			}
			continue
		}
		if job == nil {
			continue
		}
		w.process(ctx, job)
	}
}

// ErrCancelled marks a user-requested abort: the job fails but the meeting
// keeps its previous transcript and status.
var ErrCancelled = errors.New("已取消")

func (w *Worker) process(ctx context.Context, job *store.Job) {
	log.Printf("worker: job %s (%s) for meeting %s", job.ID, job.Type, job.MeetingID)
	var err error
	if job.CancelRequested {
		err = ErrCancelled // cancelled while still queued
	} else {
		switch job.Type {
		case "summarize":
			err = w.summarizeMeeting(ctx, job)
		default:
			err = w.transcribeMeeting(ctx, job)
		}
	}
	status := "completed"
	if err != nil {
		status = "failed"
		log.Printf("worker: job %s failed: %v", job.ID, err)
		// A failed summary or a user cancel must not flip the meeting to failed.
		if job.Type != "summarize" && !errors.Is(err, ErrCancelled) {
			_ = w.st.SetMeetingStatus(ctx, job.MeetingID, "failed")
		}
	}
	if ferr := w.st.FinishJob(ctx, job.ID, status, err); ferr != nil {
		log.Printf("worker: finish job %s: %v", job.ID, ferr)
	}
	// A fresh transcript (import / retranscribe) earns the meeting its
	// first summary; MaybeAutoSummarize is the one that decides whether
	// this meeting still needs one.
	if status == "completed" && job.Type != "summarize" {
		MaybeAutoSummarize(ctx, w.st, w.hub, job.MeetingID)
	}
	w.emit(job, status, 100, errString(err))
}

// Runtime is the effective transcription setup for one request cycle:
// UI-saved settings with .env deployment defaults filled into empty
// fields, plus the current voiceprint roster. Rebuilt per pass so mid-
// meeting speaker enrollment takes effect on the very next chunk.
type Runtime struct {
	// Client is the batch service (import / retranscribe / tail pass).
	Client *transcribe.Client
	// LiveClient is the live-subtitle service; it may be a completely
	// separate deployment (own base URL / model / key), with empty fields
	// falling back to the batch counterparts.
	LiveClient       *transcribe.Client
	ChunkSeconds     int
	LiveChunkSeconds int
	// Language is the default transcription language from settings.
	Language string
}

// LoadRuntime is shared by the batch worker and the live-recording handler.
// participants (when non-empty) limits which enrolled voiceprints ride
// along — the per-meeting roster beats "4 most recent" for >4-person teams.
func LoadRuntime(ctx context.Context, cfg config.Config, st *store.Store, participants []string) (*Runtime, error) {
	ts, err := st.GetTranscribeSettings(ctx)
	if err != nil {
		return nil, err
	}
	baseURL := firstNonEmpty(ts.BaseURL, cfg.TranscribeBaseURL)
	model := firstNonEmpty(ts.Model, cfg.TranscribeModel)
	apiKey := firstNonEmpty(ts.APIKey, cfg.TranscribeAPIKey)
	if baseURL == "" {
		return nil, fmt.Errorf("转录服务未配置:请在设置中填写服务地址")
	}
	client := transcribe.New(baseURL, model, apiKey)
	speakers, err := st.ListSpeakers(ctx)
	if err != nil {
		log.Printf("worker: list speakers: %v", err)
	}
	pcms := loadSpeakerPCMs(ctx, st, speakers, participants)
	client.Speakers = refsFor(pcms, client.Official())
	// Hotwords carry glossary terms only — person names proved ineffective
	// as ASR bias; name normalization happens LLM-side via the roster
	// injected into the summary prompt instead.
	if cs, err := st.GetContextSettings(ctx); err == nil {
		client.Hotwords = glossaryTerms(cs.Glossary)
	}
	liveClient := transcribe.New(
		firstNonEmpty(ts.LiveBaseURL, baseURL),
		firstNonEmpty(ts.LiveModel, model),
		firstNonEmpty(ts.LiveAPIKey, apiKey),
	)
	liveClient.Speakers = refsFor(pcms, liveClient.Official())
	liveClient.Hotwords = client.Hotwords
	rt := &Runtime{Client: client, LiveClient: liveClient, ChunkSeconds: ts.ChunkSeconds, LiveChunkSeconds: ts.LiveChunkSeconds, Language: ts.Language}
	if rt.ChunkSeconds <= 0 {
		rt.ChunkSeconds = cfg.ChunkSeconds
	}
	if rt.LiveChunkSeconds <= 0 {
		rt.LiveChunkSeconds = cfg.LiveChunkSeconds
	}
	if rt.Language == "" {
		rt.Language = "zh"
	}
	return rt, nil
}

// glossaryTerms extracts the hotword column from the glossary: each line
// is `热词(常被误转录为:错1、错2)` or a bare term; the term goes to the
// transcription service as a decoding-bias hotword, the wrong forms stay
// summary-side context only.
func glossaryTerms(glossary string) []string {
	var terms []string
	for _, line := range strings.Split(glossary, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if i := strings.LastIndex(line, "(常被误转录为:"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line != "" && !strings.ContainsAny(line, ",,") {
			terms = append(terms, line)
		}
	}
	return terms
}

type speakerPCM struct {
	name string
	pcm  []byte
}

// loadSpeakerPCMs loads each participant's single voiceprint sample (the
// newest annotation, 2-10s — experiments showed longer references do not
// help). No participants selected = no voiceprints: the service then
// returns raw letter labels instead of guessing absent people's names.
func loadSpeakerPCMs(ctx context.Context, st *store.Store, speakers []store.Speaker, participants []string) []speakerPCM {
	if len(participants) == 0 {
		return nil
	}
	wanted := map[string]bool{}
	for _, name := range participants {
		wanted[name] = true
	}
	out := make([]speakerPCM, 0, 16)
	for _, sp := range speakers {
		if len(out) == 16 { // local service hard limit
			break
		}
		match := wanted[sp.Name]
		for _, a := range sp.Aliases {
			match = match || wanted[a]
		}
		if !match {
			continue
		}
		clips, err := st.ListSpeakerClips(ctx, sp.ID)
		if err != nil || len(clips) == 0 {
			continue
		}
		wav, err := os.ReadFile(clips[0].AudioPath) // newest first
		if err != nil {
			log.Printf("worker: speaker %q sample unreadable: %v", sp.Name, err)
			continue
		}
		pcm, err := transcribe.PCMFromWAV(wav)
		if err != nil || len(pcm) == 0 {
			continue
		}
		// Meetings label people by nickname, so that's what the service is
		// told to return.
		out = append(out, speakerPCM{name: sp.DisplayName(), pcm: pcm})
	}
	return out
}

// refsFor turns samples into request references; samples are already
// capped at 10s so both local services and the official API accept them.
func refsFor(pcms []speakerPCM, official bool) []transcribe.SpeakerRef {
	_ = official
	capBytes := 10 * transcribe.SampleRate * transcribe.BytesPerSample
	refs := make([]transcribe.SpeakerRef, 0, len(pcms))
	for _, sp := range pcms {
		pcm := sp.pcm
		if len(pcm) > capBytes {
			pcm = pcm[:capBytes]
		}
		refs = append(refs, transcribe.SpeakerRef{
			Name:    sp.name,
			DataURL: "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(transcribe.EncodeWAV(pcm, transcribe.SampleRate)),
		})
	}
	return refs
}

func (w *Worker) transcribeMeeting(ctx context.Context, job *store.Job) (err error) {
	meeting, err := w.st.GetMeeting(ctx, job.MeetingID)
	if err != nil {
		return err
	}
	rt, err := LoadRuntime(ctx, w.cfg, w.st, meeting.Participants)
	if err != nil {
		return err
	}
	client := rt.Client
	if meeting.AudioPath == nil {
		return fmt.Errorf("meeting has no audio file")
	}
	if err := w.st.SetMeetingStatus(ctx, meeting.ID, "processing"); err != nil {
		return err
	}
	w.progress(ctx, job, 5, "正在预处理音频")

	pcm, duration, err := audio.NormalizeToPCM16k(ctx, *meeting.AudioPath)
	if err != nil {
		return err
	}
	if err := w.st.SetMeetingDuration(ctx, meeting.ID, duration); err != nil {
		return err
	}

	if err := client.Healthy(ctx); err != nil {
		return err
	}
	w.progress(ctx, job, 10, "转录服务已就绪,开始转录")

	language := ""
	if meeting.Language != nil {
		language = *meeting.Language
	}

	// Record every outgoing request (incl. retries) for the debug view,
	// plus a job-total record on exit.
	jobStart := time.Now()
	reqNo := 0
	client.Debug = func(info map[string]any) {
		reqNo++
		info["kind"] = "batch"
		info["request"] = reqNo
		if b, jerr := json.Marshal(info); jerr == nil {
			if derr := w.st.AddTranscribeDebug(ctx, meeting.ID, string(b)); derr != nil {
				log.Printf("worker: add transcribe debug: %v", derr)
			}
		}
	}
	defer func() {
		total := map[string]any{
			"kind": "batch-total", "jobType": job.Type, "requests": reqNo,
			"model": client.Model, "url": client.BaseURL,
			"elapsedMs": time.Since(jobStart).Milliseconds(),
		}
		if err != nil {
			total["error"] = err.Error()
		} else {
			total["status"] = "completed"
		}
		if b, jerr := json.Marshal(total); jerr == nil {
			_ = w.st.AddTranscribeDebug(ctx, meeting.ID, string(b))
		}
	}()

	// Cooperative cancel: a watcher polls the DB flag and cancels the job
	// context, which aborts the in-flight chunk request too.
	jctx, cancelJob := context.WithCancel(ctx)
	defer cancelJob()
	watcherDone := make(chan struct{})
	defer close(watcherDone)
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-watcherDone:
				return
			case <-jctx.Done():
				return
			case <-t.C:
				if c, err := w.st.JobCancelRequested(ctx, job.ID); err == nil && c {
					cancelJob()
					return
				}
			}
		}
	}()

	segs, err := transcribe.TranscribeAll(jctx, client, pcm, language, rt.ChunkSeconds,
		func(done, total int) {
			pct := 10 + 85*done/total
			w.progress(ctx, job, pct, fmt.Sprintf("已转录 %d/%d 段", done, total))
		})
	if err != nil {
		if jctx.Err() != nil && ctx.Err() == nil {
			// User cancel: keep the previous transcript and status.
			prev, _ := w.st.GetSegments(ctx, meeting.ID)
			status := "created"
			if len(prev) > 0 {
				status = "completed"
			}
			_ = w.st.SetMeetingStatus(ctx, meeting.ID, status)
			return ErrCancelled
		}
		return err
	}

	stored := make([]store.TranscriptSegment, 0, len(segs))
	for _, s := range segs {
		speaker := s.Speaker
		stored = append(stored, store.TranscriptSegment{
			MeetingID: meeting.ID,
			StartMs:   s.StartMs,
			EndMs:     s.EndMs,
			Speaker:   &speaker,
			Text:      s.Text,
			WindowIdx: s.WindowIdx,
		})
	}
	if err := w.st.ReplaceSegments(ctx, meeting.ID, stored); err != nil {
		return err
	}
	return w.st.SetMeetingStatus(ctx, meeting.ID, "completed")
}

// summarizeMeeting renders the transcript as "[mm:ss] SNN: text" lines and
// asks the configured LLM to rewrite it through the chosen template.
func (w *Worker) summarizeMeeting(ctx context.Context, job *store.Job) error {
	ss, err := w.st.GetSummarySettings(ctx)
	if err != nil {
		return err
	}
	if ss.BaseURL == "" {
		err := fmt.Errorf("总结 LLM 未配置:请在设置中填写服务地址")
		_ = w.st.FinishSummary(ctx, job.MeetingID, "", "", err)
		return err
	}
	sum, err := w.st.GetSummary(ctx, job.MeetingID)
	if err != nil {
		return err
	}
	tpl, err := w.st.GetTemplate(ctx, sum.Template)
	if err != nil {
		err := fmt.Errorf("总结模板不存在: %s", sum.Template)
		_ = w.st.FinishSummary(ctx, job.MeetingID, "", "", err)
		return err
	}
	segs, err := w.st.GetSegments(ctx, job.MeetingID)
	if err != nil {
		return err
	}
	if len(segs) == 0 {
		err := fmt.Errorf("该会议还没有转录内容")
		_ = w.st.FinishSummary(ctx, job.MeetingID, "", "", err)
		return err
	}

	var b strings.Builder
	for _, s := range segs {
		sp := ""
		if s.Speaker != nil && *s.Speaker != "" {
			sp = *s.Speaker + ": "
		}
		fmt.Fprintf(&b, "[%02d:%02d] %s%s\n", s.StartMs/60000, s.StartMs/1000%60, sp, s.Text)
	}

	prompt := tpl.Prompt
	if meeting, err := w.st.GetMeeting(ctx, job.MeetingID); err == nil && strings.TrimSpace(meeting.Context) != "" {
		prompt += "\n\n本场会议的背景信息(日历邀请/议程/参会人等,生成时充分利用其中的结构化信息):\n" + meeting.Context
	}
	// Global background was superseded by per-meeting context; only the
	// glossary remains a global concern.
	if cs, err := w.st.GetContextSettings(ctx); err == nil && strings.TrimSpace(cs.Glossary) != "" {
		prompt += "\n\n专属名词表(转录中的相近词请按此表纠正):\n" + cs.Glossary
	}
	if strings.TrimSpace(sum.CustomPrompt) != "" {
		prompt += "\n\n用户补充的会议背景(生成时参考):\n" + sum.CustomPrompt
	}
	// Roster injection: participants' names come from voiceprints and are
	// always right, but MENTIONED people's names are raw ASR output —
	// give the LLM the roster so near-homophones normalize to canonical
	// spellings.
	if speakers, err := w.st.ListSpeakers(ctx); err == nil && len(speakers) > 0 {
		prompt += "\n\n团队人员名册(转录中发音相近的人名请归一为此名册中的标准写法;确定不在名册中的人名保持原样):"
		for _, sp := range speakers {
			prompt += "\n- " + sp.Name
			if len(sp.Aliases) > 0 {
				prompt += "(别名: " + strings.Join(sp.Aliases, " / ") + ")"
			}
		}
	}

	if comments, err := w.st.ListComments(ctx, job.MeetingID); err == nil && len(comments) > 0 {
		prompt += "\n\n参会者的修订(生成时必须重点参考,这些是对转录或上一版纪要的纠正、评论与补充):"
		for _, c := range comments {
			author := c.Author
			if author == "" {
				author = "参会者"
			}
			quote := c.Quote
			if len([]rune(quote)) > 60 {
				quote = string([]rune(quote)[:60]) + "…"
			}
			switch {
			case quote == "":
				prompt += fmt.Sprintf("\n- %s 备注: %s", author, c.Body)
			case c.Kind == "comment":
				prompt += fmt.Sprintf("\n- %s 评论“%s”: %s", author, quote, c.Body)
			case c.Kind == "delete":
				prompt += fmt.Sprintf("\n- %s 要求删除“%s”(该内容不要出现在纪要中)", author, quote)
			default: // rewrite
				prompt += fmt.Sprintf("\n- %s 将“%s”改写为: %s", author, quote, c.Body)
			}
		}
	}

	if err := w.st.SetSummaryDebug(ctx, job.MeetingID, prompt, b.String()); err != nil {
		log.Printf("worker: save summary debug: %v", err)
	}
	w.progress(ctx, job, 20, "正在调用 LLM 生成总结("+tpl.Name+")")

	// Cooperative cancel: poll the flag and cancel the LLM call context.
	jctx, cancelJob := context.WithCancel(ctx)
	defer cancelJob()
	watcherDone := make(chan struct{})
	defer close(watcherDone)
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-watcherDone:
				return
			case <-jctx.Done():
				return
			case <-t.C:
				if c, err := w.st.JobCancelRequested(ctx, job.ID); err == nil && c {
					cancelJob()
					return
				}
			}
		}
	}()

	llmStart := time.Now()
	client := summarize.New(ss.BaseURL, ss.Model, ss.APIKey)
	content, genErr := client.Chat(jctx, prompt, b.String())
	if jctx.Err() != nil && ctx.Err() == nil {
		genErr = ErrCancelled
	}

	// Debug history: every trigger keeps its prompt, params and duration.
	status := "completed"
	if genErr != nil {
		status = "failed"
	}
	info := map[string]any{
		"kind": "summarize", "template": tpl.ID, "templateName": tpl.Name,
		"baseUrl": ss.BaseURL, "model": ss.Model,
		"promptChars": len([]rune(prompt)), "inputChars": len([]rune(b.String())),
		"elapsedMs": time.Since(llmStart).Milliseconds(), "status": status,
		"prompt": prompt, "input": b.String(),
	}
	if genErr != nil {
		info["error"] = genErr.Error()
	}
	if bts, jerr := json.Marshal(info); jerr == nil {
		if derr := w.st.AddSummaryDebug(ctx, job.MeetingID, string(bts)); derr != nil {
			log.Printf("worker: add summary debug: %v", derr)
		}
	}

	if ferr := w.st.FinishSummary(ctx, job.MeetingID, content, ss.Model, genErr); ferr != nil {
		return ferr
	}
	w.hub.Broadcast(map[string]any{
		"type": "summary-update", "meetingId": job.MeetingID, "status": status,
	})

	// Auto-title AFTER the summary lands so naming can use the best source
	// (background info > fresh summary > transcript). Manual renames are
	// never overwritten.
	if genErr == nil && !meetingTitleCustomized(ctx, w.st, job.MeetingID) {
		if title, applied, terr := AutoTitle(ctx, w.st, job.MeetingID, false); terr != nil {
			log.Printf("worker: auto title: %v", terr)
		} else if applied {
			w.hub.Broadcast(map[string]any{
				"type": "meeting-updated", "meetingId": job.MeetingID, "title": title,
			})
		}
	}
	return genErr
}

func (w *Worker) progress(ctx context.Context, job *store.Job, pct int, msg string) {
	if err := w.st.UpdateJobProgress(ctx, job.ID, pct); err != nil {
		log.Printf("worker: update progress: %v", err)
	}
	w.emit(job, "running", pct, msg)
}

func (w *Worker) emit(job *store.Job, status string, pct int, msg string) {
	w.hub.Broadcast(map[string]any{
		"type":      "job-progress",
		"jobId":     job.ID,
		"meetingId": job.MeetingID,
		"jobType":   job.Type,
		"status":    status,
		"progress":  pct,
		"message":   msg,
	})
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
