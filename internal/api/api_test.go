package api

import (
	"encoding/json"
	"testing"
	"time"
)

func decodeList(t *testing.T, body string) []Deployment {
	t.Helper()
	var out struct {
		Deployments []Deployment `json:"deployments"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out.Deployments
}

func TestDecodeDeployments(t *testing.T) {
	body := `{"deployments":[{
		"uid":"dpl_1","name":"api","url":"api-x.vercel.sh",
		"state":"READY","target":"production",
		"created":1700000000000,"ready":1700000048000,"buildingAt":1700000000000,
		"creator":{"username":"shehjad"},
		"meta":{"githubCommitRef":"main","githubCommitSha":"a1b2c3d4e5f6789"},
		"aFutureField":{"anything":true}
	}]}`
	d := decodeList(t, body)[0]
	if d.Status() != "ready" {
		t.Errorf("Status() = %q, want ready", d.Status())
	}
	if d.Branch() != "main" || d.ShortSHA() != "a1b2c3d" {
		t.Errorf("git meta wrong: %q %q", d.Branch(), d.ShortSHA())
	}
	if d.Duration() != 48*time.Second {
		t.Errorf("Duration() = %v, want 48s", d.Duration())
	}
}

func TestDecodeReadyStateFallback(t *testing.T) {
	d := decodeList(t, `{"deployments":[{"uid":"dpl_2","name":"web","readyState":"BUILDING"}]}`)
	if d[0].Status() != "building" {
		t.Errorf("Status() = %q, want building", d[0].Status())
	}
}

func TestMsTimeBothShapes(t *testing.T) {
	d := decodeList(t, `{"deployments":[
		{"uid":"a","createdAt":1700000000000},
		{"uid":"b","createdAt":"2023-11-14T22:13:20Z"}
	]}`)
	if d[0].CreatedMs() != 1700000000000 {
		t.Errorf("numeric createdAt = %d", d[0].CreatedMs())
	}
	if d[1].CreatedMs() != 1700000000000 {
		t.Errorf("ISO createdAt = %d", d[1].CreatedMs())
	}
}

func TestParseEventsBothShapes(t *testing.T) {
	array := []byte(`[{"type":"stdout","payload":{"text":"line one\n"}},{"type":"stderr","payload":{"text":"oops"}}]`)
	events, err := parseEvents(array)
	if err != nil || len(events) != 2 || events[1].Payload.Text != "oops" {
		t.Fatalf("array shape: events=%v err=%v", events, err)
	}

	ndjson := []byte("{\"type\":\"stdout\",\"payload\":{\"text\":\"a\"}}\n\nnot json\n{\"type\":\"stdout\",\"payload\":{\"text\":\"b\"}}\n")
	events, err = parseEvents(ndjson)
	if err != nil || len(events) != 2 || events[0].Payload.Text != "a" {
		t.Fatalf("ndjson shape: events=%v err=%v", events, err)
	}
}
