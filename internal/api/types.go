package api

import (
	"strconv"
	"strings"
	"time"
)

// Deployment covers both the v6 list item and the v13 detail object.
// Unknown JSON fields are ignored, so additive API changes never break us.
type Deployment struct {
	UID        string `json:"uid"`
	ID         string `json:"id"` // create/detail responses use id, lists use uid
	Name       string `json:"name"`
	URL        string `json:"url"`
	State      string `json:"state"`
	ReadyState string `json:"readyState"`
	Target     string `json:"target"`
	Created    int64  `json:"created"`
	CreatedAt  msTime `json:"createdAt"`
	Ready      int64  `json:"ready"`
	ReadyAt    msTime `json:"readyAt"`
	BuildingAt int64  `json:"buildingAt"`
	Creator    struct {
		Username string `json:"username"`
	} `json:"creator"`
	Meta  map[string]string `json:"meta"`
	Alias []string          `json:"alias"`
}

// Key returns whichever deployment identifier the response carried.
func (d Deployment) Key() string {
	if d.UID != "" {
		return d.UID
	}
	return d.ID
}

// Status normalizes the mixed-case states the API returns across versions.
func (d Deployment) Status() string {
	s := d.State
	if s == "" {
		s = d.ReadyState
	}
	return strings.ToLower(s)
}

func (d Deployment) Branch() string { return d.Meta["githubCommitRef"] }

func (d Deployment) SHA() string { return d.Meta["githubCommitSha"] }

func (d Deployment) ShortSHA() string {
	sha := d.SHA()
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func (d Deployment) Message() string { return d.Meta["githubCommitMessage"] }

func (d Deployment) CreatedMs() int64 {
	if d.Created > 0 {
		return d.Created
	}
	return int64(d.CreatedAt)
}

func (d Deployment) ReadyMs() int64 {
	if d.Ready > 0 {
		return d.Ready
	}
	return int64(d.ReadyAt)
}

// Duration is the build time when both timestamps exist.
func (d Deployment) Duration() time.Duration {
	start := d.BuildingAt
	if start == 0 {
		start = d.CreatedMs()
	}
	if start == 0 || d.ReadyMs() == 0 {
		return 0
	}
	return time.Duration(d.ReadyMs()-start) * time.Millisecond
}

// msTime tolerates the two timestamp shapes the API emits across
// versions: epoch milliseconds and RFC3339 strings.
type msTime int64

func (t *msTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" || s == "" {
		*t = 0
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*t = msTime(n)
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		*t = 0 // tolerate unknown future shapes rather than fail the decode
		return nil
	}
	*t = msTime(parsed.UnixMilli())
	return nil
}

type Project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Framework string `json:"framework"`
	UpdatedAt int64  `json:"updatedAt"`
	Link      struct {
		Type string `json:"type"`
		Org  string `json:"org"`
		Repo string `json:"repo"`
	} `json:"link"`
}

func (p Project) Repo() string {
	if p.Link.Repo == "" {
		return ""
	}
	return p.Link.Org + "/" + p.Link.Repo
}

type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type User struct {
	Username string `json:"username"`
	UID      string `json:"uid"`
}

type Event struct {
	Type    string `json:"type"`
	Created int64  `json:"created"`
	Payload struct {
		Text string `json:"text"`
	} `json:"payload"`
}
