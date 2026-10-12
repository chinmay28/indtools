package gen

import (
	"fmt"
	"strings"
)

// Prompts are modelled on easy-dataset's: one call asks for N questions
// about a chunk as a JSON array; a second call answers one question using
// only the chunk, optionally with explicit reasoning in tags.

const questionSystem = `You are an expert at writing training data for language models. You read a passage of text and write questions that a knowledgeable reader could answer using only that passage.`

func questionPrompt(chunk string, n int, lang string) string {
	return fmt.Sprintf(`Read the following text and write exactly %d distinct questions that can be answered from it.

Rules:
- Each question must be fully answerable from the text alone and must be self-contained: do not refer to "the text", "the passage", "the author" or "this section". Include the names, terms or context needed to understand the question on its own.
- Prefer questions about facts, definitions, mechanisms, reasons, comparisons and consequences described in the text. Avoid trivial yes/no questions.
- Vary the question types and cover different parts of the text.
- Write the questions in %s.
- Output ONLY a JSON array of strings, with no commentary, no numbering and no code fences.

Text:
"""
%s
"""`, n, languageName(lang), chunk)
}

const answerSystem = `You are a careful, precise subject-matter expert writing reference answers for a training dataset. Answer using only the information in the provided text.`

func answerPrompt(chunk, question, lang string, cot bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `Use the reference text below to answer the question.

Rules:
- Base the answer strictly on the reference text; do not add outside facts. If the text does not contain enough information, say what the text does and does not establish.
- Write a complete, well-structured answer in %s. Do not mention "the text", "the passage" or "the reference"; state the facts directly.
- Do not restate the question.
`, languageName(lang))
	if cot {
		b.WriteString(`- First think through the problem inside <reasoning></reasoning> tags: identify the relevant parts of the text and work out the answer step by step. Then give the final answer inside <answer></answer> tags. Output nothing outside these two tag pairs.
`)
	} else {
		b.WriteString(`- Output only the answer, with no preamble.
`)
	}
	fmt.Fprintf(&b, `
Reference text:
"""
%s
"""

Question: %s`, chunk, question)
	return b.String()
}

// languageName maps a BCP-47-ish code to the name the prompt uses. Unknown
// codes are passed through, so "-lang French" works too.
func languageName(code string) string {
	switch strings.ToLower(code) {
	case "", "en", "english":
		return "English"
	case "zh", "zh-cn", "chinese":
		return "Chinese"
	case "es", "spanish":
		return "Spanish"
	case "fr", "french":
		return "French"
	case "de", "german":
		return "German"
	case "ja", "japanese":
		return "Japanese"
	case "ko", "korean":
		return "Korean"
	case "pt", "portuguese":
		return "Portuguese"
	case "ru", "russian":
		return "Russian"
	case "hi", "hindi":
		return "Hindi"
	case "it", "italian":
		return "Italian"
	}
	return code
}
