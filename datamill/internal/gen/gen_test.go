package gen

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chinmay28/indtools/datamill/internal/llm"
	"github.com/chinmay28/indtools/datamill/internal/store"
)

type fake struct {
	mu    sync.Mutex
	calls int32
	fn    func(req llm.Request) (string, error)
	seen  []llm.Request
}

func (f *fake) Model() string { return "fake-model" }
func (f *fake) Complete(ctx context.Context, req llm.Request) (string, error) {
	atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	f.seen = append(f.seen, req)
	f.mu.Unlock()
	return f.fn(req)
}

func TestParseQuestions(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`["What is A?", "What is B?"]`, []string{"What is A?", "What is B?"}},
		{"Here you go:\n```json\n[\"Q1\", \"Q2\"]\n```\nHope that helps.", []string{"Q1", "Q2"}},
		{`Sure! ["Only one?"] done`, []string{"Only one?"}},
		{`[{"question":"From object?"},{"q":"Short key?"},{"other":"ignored"}]`, []string{"From object?", "Short key?"}},
		{`["dup", "DUP", "  ", "other"]`, []string{"dup", "other"}},
		{"1. First question?\n2) Second question?\n- Third one?\nnot a list line", []string{"First question?", "Second question?", "Third one?"}},
		{"<think>\nMaybe [\"not this\"]\n</think>\n[\"After thinking?\"]", []string{"After thinking?"}},
		{"[\"unterminated\"", nil},
		{"", nil},
	}
	for _, c := range cases {
		got, err := ParseQuestions(c.in)
		if c.want == nil {
			if err == nil {
				t.Errorf("ParseQuestions(%q): expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil || strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("ParseQuestions(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestParseAnswer(t *testing.T) {
	cases := []struct {
		in        string
		reasoning string
		answer    string
	}{
		{"Plain answer.", "", "Plain answer."},
		{"<reasoning>\nbecause\n</reasoning>\n<answer>42</answer>", "because", "42"},
		{"<think>hmm</think>\nThe answer is 7.", "hmm", "The answer is 7."},
		{"<reasoning>r</reasoning><answer>unterminated", "r", "unterminated"},
		{"<answer> spaced </answer> trailing junk", "", "spaced"},
	}
	for _, c := range cases {
		got := ParseAnswer(c.in)
		if got.Reasoning != c.reasoning || got.Text != c.answer {
			t.Errorf("ParseAnswer(%q) = %+v", c.in, got)
		}
	}
}

func TestQuestionsPerChunk(t *testing.T) {
	for in, want := range map[int]int{0: 1, 100: 1, 1500: 5, 9000: 10} {
		if got := QuestionsPerChunk(TaskQuestion, in); got != want {
			t.Errorf("QuestionsPerChunk(%d) = %d, want %d", in, got, want)
		}
	}
	for _, task := range []Task{TaskPrompt, TaskReply} {
		if got := QuestionsPerChunk(task, 9000); got != 1 {
			t.Errorf("QuestionsPerChunk(%s, 9000) = %d, want 1", task, got)
		}
	}
}

func TestGeneratorQuestionsAndAnswer(t *testing.T) {
	f := &fake{fn: func(req llm.Request) (string, error) {
		if strings.Contains(req.User, "write exactly 2 distinct questions") {
			return `["Q1?", "Q2?", "Q3?"]`, nil
		}
		if strings.Contains(req.User, "Question: Q1?") {
			if strings.Contains(req.User, "<reasoning>") {
				return "<reasoning>step</reasoning><answer>A1</answer>", nil
			}
			return "A1 plain", nil
		}
		return "", errors.New("unexpected prompt: " + req.User)
	}}
	g := &Generator{Client: f, Language: "fr", ReasoningEffort: "none"}
	qs, err := g.Questions(context.Background(), "chunk text", nil, 2)
	if err != nil || len(qs) != 2 { // truncated to n
		t.Fatalf("%v %v", qs, err)
	}
	if !strings.Contains(f.seen[0].User, "French") || !strings.Contains(f.seen[0].User, "chunk text") || f.seen[0].ReasoningEffort != "none" {
		t.Fatalf("prompt: %+v", f.seen[0])
	}
	if strings.Contains(f.seen[0].User, "About this text") {
		t.Fatalf("no meta block expected: %s", f.seen[0].User)
	}
	a, err := g.Answer(context.Background(), "chunk text", "Q1?", true)
	if err != nil || a.Text != "A1" || a.Reasoning != "step" {
		t.Fatalf("%+v %v", a, err)
	}
	a, err = g.Answer(context.Background(), "chunk text", "Q1?", false)
	if err != nil || a.Text != "A1 plain" || a.Reasoning != "" {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestGeneratorTasks(t *testing.T) {
	f := &fake{fn: func(req llm.Request) (string, error) { return `["turn one", "turn two"]`, nil }}
	meta := map[string]string{"recipient": "Alasinga", "date": "1894-05-28"}
	cases := []struct {
		task       Task
		wantSystem string
		wantUser   []string
	}{
		{TaskQuestion, "write questions", []string{"write exactly 1 distinct questions", "About this text:\ndate: 1894-05-28\nrecipient: Alasinga", "Text:"}},
		{TaskPrompt, "reconstruct the request", []string{"Write exactly 1 distinct requests", "About this text:", "Text:"}},
		{TaskReply, "reconstruct the message", []string{"Write exactly 1 distinct messages", "About this reply:", "Reply:"}},
	}
	for _, c := range cases {
		f.seen = nil
		g := &Generator{Client: f, Task: c.task}
		qs, err := g.Questions(context.Background(), "the chunk", meta, 1)
		if err != nil || len(qs) != 1 || qs[0] != "turn one" {
			t.Fatalf("%s: %v %v", c.task, qs, err)
		}
		req := f.seen[0]
		if !strings.Contains(req.System, c.wantSystem) {
			t.Errorf("%s: system prompt %q", c.task, req.System)
		}
		for _, w := range c.wantUser {
			if !strings.Contains(req.User, w) {
				t.Errorf("%s: user prompt lacks %q:\n%s", c.task, w, req.User)
			}
		}
		if !strings.Contains(req.User, "English") || !strings.HasSuffix(strings.TrimSpace(req.User), "the chunk\n\"\"\"") {
			t.Errorf("%s: user prompt tail: %q", c.task, req.User)
		}
	}
	if _, err := (&Generator{Client: f, Task: "bogus"}).Questions(context.Background(), "x", nil, 1); err == nil {
		t.Fatal("unknown task accepted")
	}
	if !ValidTask("") || !ValidTask(TaskReply) || ValidTask("bogus") {
		t.Fatal("ValidTask")
	}
	if !Task("").HasAnswerStage() || !TaskQuestion.HasAnswerStage() || TaskPrompt.HasAnswerStage() || TaskReply.HasAnswerStage() {
		t.Fatal("HasAnswerStage")
	}
}

func TestRunReverseTaskSkipsAnswers(t *testing.T) {
	p := seedProject(t, 2)
	f := &fake{fn: func(req llm.Request) (string, error) { return `["Write me a story."]`, nil }}
	g := &Generator{Client: f, Task: TaskPrompt}
	st, err := Run(context.Background(), p, g, RunOptions{Stage: StageAll})
	if err != nil || st.QuestionsGenerated != 2 || st.AnswersGenerated != 0 || atomic.LoadInt32(&f.calls) != 2 {
		t.Fatalf("%+v %v calls=%d", st, err, f.calls)
	}
	if _, err := Run(context.Background(), p, g, RunOptions{Stage: StageAnswers}); err == nil {
		t.Fatal("answers stage should be refused for a reverse task")
	}
	if _, err := Run(context.Background(), p, &Generator{Client: f, Task: "bogus"}, RunOptions{}); err == nil {
		t.Fatal("unknown task should be refused")
	}
}

func seedProject(t *testing.T, nChunks int) *store.Project {
	t.Helper()
	p, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var cs []store.Chunk
	for i := 0; i < nChunks; i++ {
		cs = append(cs, store.Chunk{ID: store.ChunkID("src", i), SourceID: "src", Index: i, Text: strings.Repeat("Chunk text. ", 30)})
	}
	if err := p.AppendChunks(cs); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunEndToEndAndResume(t *testing.T) {
	p := seedProject(t, 5)
	var failOnce int32 = 1
	f := &fake{fn: func(req llm.Request) (string, error) {
		if strings.Contains(req.System, "write questions") || strings.Contains(req.User, "distinct questions") {
			if strings.Contains(req.User, "Chunk text") && atomic.CompareAndSwapInt32(&failOnce, 1, 0) {
				return "", errors.New("transient")
			}
			return `["Q-a?", "Q-b?"]`, nil
		}
		return "<reasoning>r</reasoning><answer>A</answer>", nil
	}}
	g := &Generator{Client: f}
	st, err := Run(context.Background(), p, g, RunOptions{Workers: 3, QuestionsPerChunk: 2, CoT: true})
	if err != nil {
		t.Fatal(err)
	}
	// One chunk failed its question call: 4 chunks done, 8 questions, 8 answers.
	if st.ChunksProcessed != 4 || st.QuestionsGenerated != 8 || st.AnswersGenerated != 8 || st.Failed != 1 {
		t.Fatalf("stats %+v", st)
	}
	status, _ := p.Status()
	if status.ChunksWithoutQuestions != 1 || status.QuestionsWithoutAnswers != 0 {
		t.Fatalf("status %+v", status)
	}

	// Re-run: only the failed chunk is processed, then its 2 answers.
	before := atomic.LoadInt32(&f.calls)
	st, err = Run(context.Background(), p, g, RunOptions{Workers: 3, QuestionsPerChunk: 2, CoT: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.ChunksProcessed != 1 || st.QuestionsGenerated != 2 || st.AnswersGenerated != 2 || st.Failed != 0 {
		t.Fatalf("resume stats %+v", st)
	}
	if calls := atomic.LoadInt32(&f.calls) - before; calls != 3 {
		t.Fatalf("expected 3 LLM calls on resume, got %d", calls)
	}
	pairs, _ := p.Pairs()
	if len(pairs) != 10 || pairs[0].Answer.Reasoning != "r" || pairs[0].Answer.Model != "fake-model" {
		t.Fatalf("pairs %d %+v", len(pairs), pairs[0].Answer)
	}
	// Question IDs are deterministic and ordered within a chunk.
	if pairs[0].Question.ID != "src-0000-q1" || pairs[1].Question.ID != "src-0000-q2" {
		t.Fatalf("ids %s %s", pairs[0].Question.ID, pairs[1].Question.ID)
	}

	// A third run has nothing to do and makes no calls.
	before = atomic.LoadInt32(&f.calls)
	if st, _ = Run(context.Background(), p, g, RunOptions{}); st.ChunksProcessed != 0 || atomic.LoadInt32(&f.calls) != before {
		t.Fatalf("idle run did work: %+v", st)
	}
}

func TestRunStagesAreSeparable(t *testing.T) {
	p := seedProject(t, 2)
	f := &fake{fn: func(req llm.Request) (string, error) { return `["Q?"]`, nil }}
	st, err := Run(context.Background(), p, &Generator{Client: f}, RunOptions{Stage: StageQuestions})
	if err != nil || st.QuestionsGenerated != 2 || st.AnswersGenerated != 0 {
		t.Fatalf("%+v %v", st, err)
	}
	f.fn = func(req llm.Request) (string, error) { return "an answer", nil }
	st, err = Run(context.Background(), p, &Generator{Client: f}, RunOptions{Stage: StageAnswers})
	if err != nil || st.QuestionsGenerated != 0 || st.AnswersGenerated != 2 {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestRunCancellation(t *testing.T) {
	p := seedProject(t, 50)
	ctx, cancel := context.WithCancel(context.Background())
	f := &fake{fn: func(req llm.Request) (string, error) {
		cancel()
		return `["Q?"]`, nil
	}}
	st, err := Run(ctx, p, &Generator{Client: f}, RunOptions{Workers: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if st.ChunksProcessed >= 50 {
		t.Fatalf("should have stopped early: %+v", st)
	}
}
