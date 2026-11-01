package web

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/yosukeno/pogo/schedule"
)

func TestSchedulePreviewShowsSelectedZoneWithoutCreatingOrRunningTasks(t *testing.T) {
	server, dir := scheduleTestServer(t)
	_, info := scheduleRequest(t, server, "POST", "/api/schedule/preview", `{"kind":"cron","cron":"0 9 * * *","timezone":"Asia/Shanghai"}`)
	times, ok := info["next_runs"].([]any)
	if !ok || len(times) != 3 {
		t.Fatal("preview did not return three dates", info)
	}
	zone, _ := time.LoadLocation("Asia/Shanghai")
	for i, raw := range times {
		at, err := time.Parse(time.RFC3339, raw.(string))
		if err != nil || at.In(zone).Hour() != 9 || at.In(zone).Minute() != 0 {
			t.Fatal("wrong selected-zone deadline", raw, err)
		}
		if i > 0 {
			previous, _ := time.Parse(time.RFC3339, times[i-1].(string))
			if !at.After(previous) {
				t.Fatal("preview dates are not increasing")
			}
		}
	}
	jobs, err := schedule.NewStore(dir).Load()
	if err != nil || len(jobs) != 0 {
		t.Fatal("preview saved or started a task", jobs, err)
	}
}

func TestSchedulePreviewValidatesInputAndRequiresAuthentication(t *testing.T) {
	server, _ := scheduleTestServer(t)
	for _, body := range []string{
		`{"kind":"cron","cron":"bad","timezone":"UTC"}`,
		`{"kind":"cron","cron":"0 9 * * *","timezone":"Bad/Zone"}`,
		`{"kind":"every","every_sec":9223372036854775807,"timezone":"UTC"}`,
		`{"kind":"at","at":"2020-01-01T00:00:00Z","timezone":"UTC"}`,
		`{"kind":"every","every_sec":60,"agent_id":"forged"}`,
	} {
		code, _ := scheduleRequest(t, server, "POST", "/api/schedule/preview", body)
		if code != 400 {
			t.Fatalf("body %s: got %d", body, code)
		}
	}
	response, err := http.Post(server.URL+"/api/schedule/preview", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("preview bypassed authentication", response.StatusCode)
	}
	at := time.Now().Add(time.Hour).UTC().Truncate(time.Minute).Format(time.RFC3339)
	code, info := scheduleRequest(t, server, "POST", "/api/schedule/preview", `{"kind":"at","at":`+strconv.Quote(at)+`,"timezone":"UTC"}`)
	if code != 200 || len(info["next_runs"].([]any)) != 1 {
		t.Fatal("one-shot preview must have exactly one deadline", code, info)
	}
}
