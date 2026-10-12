package gen

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/chinmay28/indtools/datamill/internal/store"
)

// Stage selects which generation steps Run performs.
type Stage string

// Stages.
const (
	StageQuestions Stage = "questions"
	StageAnswers   Stage = "answers"
	StageAll       Stage = "all"
)

// RunOptions configures the pipeline.
type RunOptions struct {
	Stage Stage
	// Workers is the number of concurrent LLM requests (default 4).
	Workers int
	// QuestionsPerChunk fixes the question count; 0 means automatic.
	QuestionsPerChunk int
	// CoT asks for reasoning with each answer.
	CoT bool
	// Log receives progress lines; nil discards them.
	Log io.Writer
}

// Stats summarises a run.
type Stats struct {
	ChunksProcessed, QuestionsGenerated  int
	QuestionsProcessed, AnswersGenerated int
	// Failed counts items that errored after retries. They are left
	// pending, so re-running the stage picks them up again.
	Failed int
}

// Run generates questions for chunks that have none, then answers for
// questions that have none, appending results to the project as they
// complete. It is resumable: interrupting and re-running continues where
// it left off. Run returns an error only for setup failures or context
// cancellation; per-item failures are counted in Stats.Failed.
func Run(ctx context.Context, p *store.Project, g *Generator, o RunOptions) (Stats, error) {
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.Stage == "" {
		o.Stage = StageAll
	}
	var st Stats
	if o.Stage == StageQuestions || o.Stage == StageAll {
		if err := runQuestions(ctx, p, g, o, &st); err != nil {
			return st, err
		}
	}
	if o.Stage == StageAnswers || o.Stage == StageAll {
		if err := runAnswers(ctx, p, g, o, &st); err != nil {
			return st, err
		}
	}
	return st, ctx.Err()
}

func runQuestions(ctx context.Context, p *store.Project, g *Generator, o RunOptions, st *Stats) error {
	chunks, err := p.Chunks()
	if err != nil {
		return err
	}
	existing, err := p.Questions()
	if err != nil {
		return err
	}
	done := make(map[string]bool, len(existing))
	for _, q := range existing {
		done[q.ChunkID] = true
	}
	var pending []store.Chunk
	for _, c := range chunks {
		if !done[c.ID] {
			pending = append(pending, c)
		}
	}
	logf(o.Log, "questions: %d chunks pending (%d already done)", len(pending), len(chunks)-len(pending))

	var mu sync.Mutex
	return forEach(ctx, o.Workers, len(pending), func(i int) {
		c := pending[i]
		qs, err := g.Questions(ctx, c.Text, o.QuestionsPerChunk)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			st.Failed++
			logf(o.Log, "chunk %s: %v", c.ID, err)
			return
		}
		recs := make([]store.Question, len(qs))
		for k, q := range qs {
			recs[k] = store.Question{ID: store.QuestionID(c.ID, k+1), ChunkID: c.ID, Text: q}
		}
		if err := p.AppendQuestions(recs); err != nil {
			st.Failed++
			logf(o.Log, "chunk %s: store: %v", c.ID, err)
			return
		}
		st.ChunksProcessed++
		st.QuestionsGenerated += len(recs)
		if st.ChunksProcessed%10 == 0 || st.ChunksProcessed == len(pending) {
			logf(o.Log, "questions: %d/%d chunks, %d questions", st.ChunksProcessed, len(pending), st.QuestionsGenerated)
		}
	})
}

func runAnswers(ctx context.Context, p *store.Project, g *Generator, o RunOptions, st *Stats) error {
	chunks, err := p.Chunks()
	if err != nil {
		return err
	}
	chunkText := make(map[string]string, len(chunks))
	for _, c := range chunks {
		chunkText[c.ID] = c.Text
	}
	qs, err := p.Questions()
	if err != nil {
		return err
	}
	as, err := p.Answers()
	if err != nil {
		return err
	}
	done := make(map[string]bool, len(as))
	for _, a := range as {
		done[a.QuestionID] = true
	}
	var pending []store.Question
	for _, q := range qs {
		if !done[q.ID] {
			pending = append(pending, q)
		}
	}
	logf(o.Log, "answers: %d questions pending (%d already done)", len(pending), len(qs)-len(pending))

	var mu sync.Mutex
	return forEach(ctx, o.Workers, len(pending), func(i int) {
		q := pending[i]
		text, ok := chunkText[q.ChunkID]
		if !ok {
			mu.Lock()
			st.Failed++
			logf(o.Log, "question %s: chunk %s not found", q.ID, q.ChunkID)
			mu.Unlock()
			return
		}
		a, err := g.Answer(ctx, text, q.Text, o.CoT)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			st.Failed++
			logf(o.Log, "question %s: %v", q.ID, err)
			return
		}
		rec := store.Answer{QuestionID: q.ID, Text: a.Text, Reasoning: a.Reasoning, Model: g.Client.Model()}
		if err := p.AppendAnswers([]store.Answer{rec}); err != nil {
			st.Failed++
			logf(o.Log, "question %s: store: %v", q.ID, err)
			return
		}
		st.QuestionsProcessed++
		st.AnswersGenerated++
		if st.AnswersGenerated%10 == 0 || st.AnswersGenerated == len(pending) {
			logf(o.Log, "answers: %d/%d", st.AnswersGenerated, len(pending))
		}
	})
}

// forEach runs fn(i) for i in [0,n) on `workers` goroutines, stopping
// early when ctx is cancelled. Items already started are allowed to finish.
func forEach(ctx context.Context, workers, n int, fn func(i int)) error {
	if n == 0 {
		return nil
	}
	if workers > n {
		workers = n
	}
	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				fn(i)
			}
		}()
	}
	var err error
feed:
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			err = ctx.Err()
			break feed
		case idx <- i:
		}
	}
	close(idx)
	wg.Wait()
	return err
}

func logf(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, format+"\n", args...)
	}
}
