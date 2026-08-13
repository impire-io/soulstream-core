package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/record"
	"github.com/impire-io/soulstream-core/topic"
)

func TestMemoryQueryTool(t *testing.T) {
	h, url := setup(t, "bookkeeper-agent")
	ctx := context.Background()

	res, _, err := h.startTopic(ctx, nil, startTopicInput{Name: "Cadence"})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(resultText(t, res))
	turnRes, _, err := h.postTurn(ctx, nil, postTurnInput{Path: path, Body: "weekly it is"})
	if err != nil {
		t.Fatal(err)
	}
	opID := strings.TrimSpace(resultText(t, turnRes))

	// Silence first: empty answers, not an error.
	res, _, err = h.memoryQuery(ctx, nil, memoryQueryInput{Query: "cadence?", TimeoutMs: 150})
	if err != nil {
		t.Fatalf("silent query: %v", err)
	}
	if out := resultText(t, res); !strings.Contains(out, `"answers": []`) {
		t.Errorf("silent query = %q, want empty answers", out)
	}

	// A witness under another persona, via the public library surface.
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	witness := clientOn(t, url, "historian")
	go func() {
		_ = topic.RespondMemory(wctx, witness, topic.MemoryWitness{
			Answer: func(topic.MemoryQueryRequest) []topic.MemoryAnswerDraft {
				return []topic.MemoryAnswerDraft{{
					Answer: "weekly, see the topic",
					Citations: []topic.MemoryCitation{
						{Topic: path, OpID: opID},
						{Topic: path, OpID: record.NewID()},
					},
				}}
			},
		})
	}()

	var out string
	for range 20 { // witness liveness: retry until the subscription serves
		res, _, err = h.memoryQuery(ctx, nil, memoryQueryInput{Query: "cadence?", TimeoutMs: 300})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		out = resultText(t, res)
		if strings.Contains(out, "historian") {
			break
		}
	}
	if !strings.Contains(out, `"witness": "historian"`) {
		t.Fatalf("attribution missing:\n%s", out)
	}
	if !strings.Contains(out, `"grade": "fact"`) || !strings.Contains(out, `"grade": "unverifiable"`) {
		t.Errorf("grades missing:\n%s", out)
	}

	// Malformed after is a loud input error.
	if _, _, err := h.memoryQuery(ctx, nil, memoryQueryInput{Query: "q", After: "yesterday"}); err == nil {
		t.Error("malformed after must error")
	}
}

func TestMemoryFetchTool(t *testing.T) {
	h, _ := setup(t, "bookkeeper-agent")
	ctx := context.Background()

	res, _, err := h.startTopic(ctx, nil, startTopicInput{Name: "Evidence"})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(resultText(t, res))
	turnRes, _, err := h.postTurn(ctx, nil, postTurnInput{Path: path, Body: "keep this"})
	if err != nil {
		t.Fatal(err)
	}
	opID := strings.TrimSpace(resultText(t, turnRes))

	// Live op: found via the stream, verdict included, exhibit attached.
	res, _, err = h.memoryFetch(ctx, nil, memoryFetchInput{Topic: path, OpID: opID, TimeoutMs: 150})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var fetched struct {
		Found   bool            `json:"found"`
		Verdict string          `json:"verdict"`
		Source  string          `json:"source"`
		Exhibit *record.Exhibit `json:"exhibit"`
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &fetched); err != nil {
		t.Fatalf("fetch result: %v", err)
	}
	if !fetched.Found || fetched.Source != "live" || fetched.Exhibit == nil {
		t.Fatalf("fetch result = %+v", fetched)
	}
	rec, err := fetched.Exhibit.Record()
	if err != nil || rec.ID != opID {
		t.Errorf("exhibit record = %+v (err %v)", rec, err)
	}

	// A vanished op with no witnesses: found=false, not an error.
	start := time.Now()
	res, _, err = h.memoryFetch(ctx, nil, memoryFetchInput{Topic: path, OpID: record.NewID(), TimeoutMs: 150})
	if err != nil {
		t.Fatalf("silent fetch: %v", err)
	}
	if !strings.Contains(resultText(t, res), `"found": false`) {
		t.Errorf("silent fetch = %q", resultText(t, res))
	}
	if time.Since(start) > 2*time.Second {
		t.Error("silent fetch must resolve near its deadline")
	}
}
