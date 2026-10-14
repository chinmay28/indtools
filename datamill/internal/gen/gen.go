// Package gen produces question/answer pairs from text chunks with an LLM.
//
// The first stage writes the user turn for a chunk. Which kind of user turn
// is the Generator's Task: a question the chunk answers (TaskQuestion, the
// default), a request the chunk fulfils (TaskPrompt: the chunk is a story,
// essay or poem and the model writes the instruction that would produce
// it), or a message the chunk replies to (TaskReply: the chunk is a letter
// or answer and the model writes what the correspondent sent). For the two
// reverse tasks the chunk itself is the assistant turn, so there is no
// answer stage.
//
// Contract: Generator methods are pure functions of (chunk, options) apart
// from the LLM call, and they parse model output defensively: code fences,
// leading prose and trailing commentary around the JSON array are
// tolerated; questions are de-duplicated and trimmed; an answer without
// the expected tags is used verbatim. A response that yields no questions
// is an error (callers may retry).
package gen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/chinmay28/indtools/datamill/internal/llm"
)

// Task selects what the question stage writes for each chunk.
type Task string

// Tasks.
const (
	// TaskQuestion writes questions the chunk answers.
	TaskQuestion Task = "question"
	// TaskPrompt writes requests for which the chunk is the response.
	TaskPrompt Task = "prompt"
	// TaskReply writes messages to which the chunk is the reply.
	TaskReply Task = "reply"
)

// Tasks lists the valid Task values.
var Tasks = []Task{TaskQuestion, TaskPrompt, TaskReply}

// ValidTask reports whether t is one of Tasks or empty.
func ValidTask(t Task) bool {
	if t == "" {
		return true
	}
	for _, k := range Tasks {
		if k == t {
			return true
		}
	}
	return false
}

// HasAnswerStage reports whether t pairs with a model-written answer.
// Only TaskQuestion does; for the reverse tasks the chunk is the answer.
func (t Task) HasAnswerStage() bool { return t == "" || t == TaskQuestion }

// Generator wraps an llm.Client with prompt settings.
type Generator struct {
	Client llm.Client
	// Task selects the user-turn prompt; "" means TaskQuestion.
	Task Task
	// Language is the language questions and answers are written in
	// ("en" by default).
	Language string
	// MaxTokens caps each completion; 0 lets the backend choose.
	MaxTokens int
	// Temperature, when non-nil, is sent to the backend.
	Temperature *float64
	// ReasoningEffort, when set, is passed to the backend to cap or
	// disable model "thinking" (see llm.Request.ReasoningEffort).
	ReasoningEffort string
}

// Answer is a generated answer with optional reasoning.
type Answer struct {
	Text      string
	Reasoning string
}

// QuestionsPerChunk picks how many user turns to ask for a chunk of the
// given rune length when the caller does not fix a number. For
// TaskQuestion it is one per ~300 runes, between 1 and 10; a story or a
// letter gets one request or one incoming message however long it is.
func QuestionsPerChunk(task Task, runes int) int {
	if !task.HasAnswerStage() {
		return 1
	}
	n := runes / 300
	if n < 1 {
		return 1
	}
	if n > 10 {
		return 10
	}
	return n
}

// Questions asks the model for n user turns about chunk, according to
// g.Task. meta (may be nil) is shown to the model as context about the
// chunk, for example a letter's recipient and date.
func (g *Generator) Questions(ctx context.Context, chunk string, meta map[string]string, n int) ([]string, error) {
	if n <= 0 {
		n = QuestionsPerChunk(g.Task, len([]rune(chunk)))
	}
	var system, user string
	switch g.Task {
	case TaskPrompt:
		system, user = promptSystem, promptPrompt(chunk, meta, n, g.Language)
	case TaskReply:
		system, user = replySystem, replyPrompt(chunk, meta, n, g.Language)
	case TaskQuestion, "":
		system, user = questionSystem, questionPrompt(chunk, meta, n, g.Language)
	default:
		return nil, fmt.Errorf("gen: unknown task %q", g.Task)
	}
	out, err := g.Client.Complete(ctx, llm.Request{
		System:          system,
		User:            user,
		MaxTokens:       g.MaxTokens,
		Temperature:     g.Temperature,
		ReasoningEffort: g.ReasoningEffort,
	})
	if err != nil {
		return nil, err
	}
	qs, err := ParseQuestions(out)
	if err != nil {
		return nil, err
	}
	if len(qs) > n {
		qs = qs[:n]
	}
	return qs, nil
}

// Answer asks the model to answer question using chunk. With cot the model
// is asked to show its reasoning, returned separately.
func (g *Generator) Answer(ctx context.Context, chunk, question string, cot bool) (Answer, error) {
	out, err := g.Client.Complete(ctx, llm.Request{
		System:          answerSystem,
		User:            answerPrompt(chunk, question, g.Language, cot),
		MaxTokens:       g.MaxTokens,
		Temperature:     g.Temperature,
		ReasoningEffort: g.ReasoningEffort,
	})
	if err != nil {
		return Answer{}, err
	}
	a := ParseAnswer(out)
	if a.Text == "" {
		return Answer{}, errors.New("gen: empty answer")
	}
	return a, nil
}

var fenceRe = regexp.MustCompile("(?s)```[a-zA-Z]*\\s*(.*?)```")

// ParseQuestions extracts a JSON array of strings from model output. It
// tolerates a leading <think> block, code fences and surrounding prose,
// falls back to a numbered or bulleted list, trims entries, drops empties
// and duplicates.
func ParseQuestions(s string) ([]string, error) {
	s = strings.TrimSpace(openTagRe.ReplaceAllString(s, ""))
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	var raw []any
	if i, j := strings.Index(s, "["), strings.LastIndex(s, "]"); i >= 0 && j > i {
		if err := json.Unmarshal([]byte(s[i:j+1]), &raw); err != nil {
			raw = nil
		}
	}
	var qs []string
	if raw != nil {
		for _, it := range raw {
			switch v := it.(type) {
			case string:
				qs = append(qs, v)
			case map[string]any:
				// {"question": "..."} objects are accepted too.
				for _, k := range []string{"question", "q", "text"} {
					if str, ok := v[k].(string); ok {
						qs = append(qs, str)
						break
					}
				}
			}
		}
	} else {
		qs = listItems(s)
	}
	qs = dedupe(qs)
	if len(qs) == 0 {
		return nil, fmt.Errorf("gen: no questions found in model output: %q", truncate(s, 200))
	}
	return qs, nil
}

var listItemRe = regexp.MustCompile(`^\s*(?:\d+[.)]|[-*•])\s+(.*\S)\s*$`)

func listItems(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if m := listItemRe.FindStringSubmatch(ln); m != nil {
			out = append(out, strings.Trim(m[1], `"'`))
		}
	}
	return out
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, q := range in {
		q = strings.TrimSpace(q)
		key := strings.ToLower(q)
		if q == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, q)
	}
	return out
}

var (
	reasoningRe = regexp.MustCompile(`(?s)<(?:reasoning|think|thinking)>(.*?)</(?:reasoning|think|thinking)>`)
	answerRe    = regexp.MustCompile(`(?s)<answer>(.*?)</answer>`)
	openTagRe   = regexp.MustCompile(`(?s)<(?:reasoning|think|thinking)>.*?</(?:reasoning|think|thinking)>\s*`)
)

// ParseAnswer splits tagged output into reasoning and answer. Without tags
// the whole output is the answer. An unterminated <answer> tag is taken to
// run to the end of the text.
func ParseAnswer(s string) Answer {
	var a Answer
	if m := reasoningRe.FindStringSubmatch(s); m != nil {
		a.Reasoning = strings.TrimSpace(m[1])
	}
	if m := answerRe.FindStringSubmatch(s); m != nil {
		a.Text = strings.TrimSpace(m[1])
		return a
	}
	rest := openTagRe.ReplaceAllString(s, "")
	if i := strings.Index(rest, "<answer>"); i >= 0 {
		rest = rest[i+len("<answer>"):]
	}
	a.Text = strings.TrimSpace(rest)
	return a
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
