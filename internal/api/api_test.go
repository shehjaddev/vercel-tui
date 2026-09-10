package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestDecodeNonStringMeta(t *testing.T) {
	d := decodeList(t, `{"deployments":[{"uid":"dpl_3","name":"web","meta":{"githubCommitRef":"main","builds":3,"flag":true}}]}`)
	if d[0].Branch() != "main" {
		t.Errorf("Branch() = %q, want main", d[0].Branch())
	}
	if d[0].Meta["builds"] != "3" || d[0].Meta["flag"] != "true" {
		t.Errorf("non-string meta not coerced: %+v", d[0].Meta)
	}
}

func TestDeploymentsPaginates(t *testing.T) {
	var untils []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		untils = append(untils, r.URL.Query().Get("until"))
		if r.URL.Query().Get("until") == "" {
			json.NewEncoder(w).Encode(map[string]any{
				"deployments": []map[string]any{{"uid": "dpl_1", "name": "web"}},
				"pagination":  map[string]any{"next": 1700000000000},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"deployments": []map[string]any{{"uid": "dpl_2", "name": "web"}},
		})
	}))
	defer srv.Close()

	c := New("tok")
	c.baseURL = srv.URL
	deps, err := c.Deployments("", "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 2 || deps[0].UID != "dpl_1" || deps[1].UID != "dpl_2" {
		t.Fatalf("paged deps = %+v", deps)
	}
	if len(untils) != 2 || untils[0] != "" || untils[1] != "1700000000000" {
		t.Fatalf("until cursors = %q", untils)
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
