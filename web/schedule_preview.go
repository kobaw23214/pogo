package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/yosukeno/pogo/schedule"
)

// Preview validates only trigger data. It neither saves a task, imports files,
// installs an OS trigger nor starts a run.
func (s *Server) handleSchedulePreview(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind     string `json:"kind"`
		Cron     string `json:"cron"`
		EverySec int64  `json:"every_sec"`
		At       string `json:"at"`
		Timezone string `json:"timezone"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		writeErr(w, 400, err)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeErr(w, 400, errors.New("预览仅接受一个时间设置对象"))
		return
	}
	now := time.Now()
	body := scheduleJobBody{Prompt: "preview", Workdir: s.mgr.Cwd(), Kind: in.Kind, Cron: in.Cron, EverySec: in.EverySec, At: in.At, Timezone: in.Timezone}
	job, next, err := body.build(now)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	times := []string{}
	for len(times) < 3 && next != nil {
		if !next.After(now) {
			writeErr(w, 400, errors.New("执行间隔超出可支持的范围"))
			return
		}
		times = append(times, next.Format(time.RFC3339))
		if job.Kind == schedule.KindAt {
			break
		}
		job.LastRunAt = *next
		now = *next
		next = schedule.NextRun(job, now)
	}
	zone, label := schedule.EffectiveTimezone(job.Timezone)
	writeJSON(w, 200, map[string]any{"next_runs": times, "timezone": zone, "timezone_label": label, "current_time": time.Now().UTC().Format(time.RFC3339)})
}
