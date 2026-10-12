// Package store persists a datamill project on disk as append-only JSONL
// files, so every stage can be re-run and resumed.
//
// Layout of a project directory:
//
//	sources.jsonl    one Source per ingested file
//	text/<id>.txt    cleaned text of each source
//	chunks.jsonl     one Chunk per text chunk
//	questions.jsonl  one Question per generated question
//	answers.jsonl    one Answer per answered question
//
// Contract: records are only ever appended; readers tolerate a missing file
// (treated as empty) and skip blank lines. IDs are deterministic:
// Source.ID is the first 12 hex digits of the file's SHA-256, Chunk.ID is
// "<source>-<index:04d>" and Question.ID is "<chunk>-q<n>". Re-ingesting
// the same file content therefore produces the same IDs, which is how
// duplicates are detected.
package store

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Source describes one ingested input file.
type Source struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Title  string `json:"title,omitempty"`
	Format string `json:"format"`
	Chars  int    `json:"chars"`
	Chunks int    `json:"chunks"`
	SHA256 string `json:"sha256"`
}

// Chunk is one piece of a source's text.
type Chunk struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
	Index    int    `json:"index"`
	Text     string `json:"text"`
}

// Question is one generated question about a chunk.
type Question struct {
	ID      string `json:"id"`
	ChunkID string `json:"chunk_id"`
	Text    string `json:"text"`
}

// Answer is the generated answer to a question.
type Answer struct {
	QuestionID string `json:"question_id"`
	Text       string `json:"text"`
	Reasoning  string `json:"reasoning,omitempty"`
	Model      string `json:"model,omitempty"`
}

// Project is a handle on a project directory. Methods are safe for
// concurrent use.
type Project struct {
	Dir string
	mu  sync.Mutex
}

// Open creates the directory if needed and returns a Project.
func Open(dir string) (*Project, error) {
	if err := os.MkdirAll(filepath.Join(dir, "text"), 0o755); err != nil {
		return nil, err
	}
	return &Project{Dir: dir}, nil
}

// SourceID derives the deterministic ID and full hash for file content.
func SourceID(content []byte) (id, sha string) {
	sum := sha256.Sum256(content)
	sha = hex.EncodeToString(sum[:])
	return sha[:12], sha
}

// ChunkID formats a chunk ID.
func ChunkID(sourceID string, index int) string {
	return fmt.Sprintf("%s-%04d", sourceID, index)
}

// QuestionID formats a question ID; n is 1-based within the chunk.
func QuestionID(chunkID string, n int) string {
	return fmt.Sprintf("%s-q%d", chunkID, n)
}

func (p *Project) path(name string) string { return filepath.Join(p.Dir, name) }

// Sources returns all ingested sources.
func (p *Project) Sources() ([]Source, error) {
	return readJSONL[Source](p.path("sources.jsonl"))
}

// Chunks returns all chunks in file order (which is source, then index).
func (p *Project) Chunks() ([]Chunk, error) {
	return readJSONL[Chunk](p.path("chunks.jsonl"))
}

// Questions returns all generated questions.
func (p *Project) Questions() ([]Question, error) {
	return readJSONL[Question](p.path("questions.jsonl"))
}

// Answers returns all generated answers.
func (p *Project) Answers() ([]Answer, error) {
	return readJSONL[Answer](p.path("answers.jsonl"))
}

// AppendSource records a source and its cleaned text.
func (p *Project) AppendSource(s Source, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := os.WriteFile(p.path(filepath.Join("text", s.ID+".txt")), []byte(text), 0o644); err != nil {
		return err
	}
	return appendJSONL(p.path("sources.jsonl"), []Source{s})
}

// AppendChunks records chunks.
func (p *Project) AppendChunks(cs []Chunk) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return appendJSONL(p.path("chunks.jsonl"), cs)
}

// AppendQuestions records questions.
func (p *Project) AppendQuestions(qs []Question) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return appendJSONL(p.path("questions.jsonl"), qs)
}

// AppendAnswers records answers.
func (p *Project) AppendAnswers(as []Answer) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return appendJSONL(p.path("answers.jsonl"), as)
}

// Text returns the cleaned text of a source.
func (p *Project) Text(sourceID string) (string, error) {
	b, err := os.ReadFile(p.path(filepath.Join("text", sourceID+".txt")))
	return string(b), err
}

// Status summarises the project's progress.
type Status struct {
	Sources, Chunks, Questions, Answers int
	// ChunksWithoutQuestions and QuestionsWithoutAnswers are the pending
	// work for the generate stage.
	ChunksWithoutQuestions  int
	QuestionsWithoutAnswers int
}

// Status computes counts of done and pending work.
func (p *Project) Status() (Status, error) {
	var st Status
	srcs, err := p.Sources()
	if err != nil {
		return st, err
	}
	chunks, err := p.Chunks()
	if err != nil {
		return st, err
	}
	qs, err := p.Questions()
	if err != nil {
		return st, err
	}
	as, err := p.Answers()
	if err != nil {
		return st, err
	}
	st.Sources, st.Chunks, st.Questions, st.Answers = len(srcs), len(chunks), len(qs), len(as)
	hasQ := make(map[string]bool, len(qs))
	for _, q := range qs {
		hasQ[q.ChunkID] = true
	}
	for _, c := range chunks {
		if !hasQ[c.ID] {
			st.ChunksWithoutQuestions++
		}
	}
	hasA := make(map[string]bool, len(as))
	for _, a := range as {
		hasA[a.QuestionID] = true
	}
	for _, q := range qs {
		if !hasA[q.ID] {
			st.QuestionsWithoutAnswers++
		}
	}
	return st, nil
}

// Pair joins a question with its answer and source chunk.
type Pair struct {
	Question Question
	Answer   Answer
	Chunk    Chunk
}

// Pairs returns every answered question joined with its chunk, ordered by
// chunk file order then question number. Questions without answers are
// omitted.
func (p *Project) Pairs() ([]Pair, error) {
	chunks, err := p.Chunks()
	if err != nil {
		return nil, err
	}
	qs, err := p.Questions()
	if err != nil {
		return nil, err
	}
	as, err := p.Answers()
	if err != nil {
		return nil, err
	}
	chunkByID := make(map[string]Chunk, len(chunks))
	chunkOrder := make(map[string]int, len(chunks))
	for i, c := range chunks {
		chunkByID[c.ID] = c
		chunkOrder[c.ID] = i
	}
	ansByQ := make(map[string]Answer, len(as))
	for _, a := range as {
		ansByQ[a.QuestionID] = a // last answer wins
	}
	var pairs []Pair
	for _, q := range qs {
		a, ok := ansByQ[q.ID]
		if !ok {
			continue
		}
		pairs = append(pairs, Pair{Question: q, Answer: a, Chunk: chunkByID[q.ChunkID]})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		return chunkOrder[pairs[i].Chunk.ID] < chunkOrder[pairs[j].Chunk.ID]
	})
	return pairs, nil
}

func appendJSONL[T any](path string, items []T) error {
	if len(items) == 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	dec := json.NewDecoder(bufio.NewReader(f))
	for {
		var it T
		err := dec.Decode(&it)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, it)
	}
}
