package gen

import (
	"fmt"
	"strings"

	"github.com/chinmay28/indtools/datamill/internal/records"
)

// Prompts are modelled on easy-dataset's: one call asks for N user turns
// about a chunk as a JSON array (questions it answers, prompts it fulfils
// or messages it replies to, by Task); a second call answers one question
// using only the chunk, optionally with explicit reasoning in tags.

const jsonArrayRule = `- Output ONLY a JSON array of strings, with no commentary, no numbering and no code fences.`

// metaBlock renders record metadata as a context paragraph, or "".
func metaBlock(label string, meta map[string]string) string {
	m := records.FormatMeta(meta)
	if m == "" {
		return ""
	}
	return label + ":\n" + m + "\n\n"
}

const questionSystem = `You are an expert at writing training data for language models. You read a passage of text and write questions that a knowledgeable reader could answer using only that passage.`

func questionPrompt(chunk string, meta map[string]string, n int, lang string) string {
	return fmt.Sprintf(`Read the following text and write exactly %d distinct questions that can be answered from it.

Rules:
- Each question must be fully answerable from the text alone and must be self-contained: do not refer to "the text", "the passage", "the author" or "this section". Include the names, terms or context needed to understand the question on its own.
- Prefer questions about facts, definitions, mechanisms, reasons, comparisons and consequences described in the text. Avoid trivial yes/no questions.
- Vary the question types and cover different parts of the text.
- Write the questions in %s.
%s

%sText:
"""
%s
"""`, n, languageName(lang), jsonArrayRule, metaBlock("About this text", meta), chunk)
}

const promptSystem = `You are an expert at writing training data for language models. You read a finished piece of writing and reconstruct the request a writer could have been given to produce it.`

func promptPrompt(chunk string, meta map[string]string, n int, lang string) string {
	return fmt.Sprintf(`The text below is a complete piece of writing. Write exactly %d distinct requests that a user could give to a writing assistant for which this text would be an excellent response.

Rules:
- Each request must stand on its own. Say what kind of text is wanted (a short story, a poem, a lecture, a letter, an essay, ...), who it is for, what it is about, the main characters, events or ideas, and the tone and approximate length, so that a writer who had never seen the text could produce something very like it.
- Describe the premise and the arc; do not copy sentences from the text and do not retell every detail.
- Do not refer to "the text", "the passage" or "the author"; write as the user making the request.
- Vary the requests: one may be brief, another may say more about the characters, setting or themes.
- Write the requests in %s.
%s

%sText:
"""
%s
"""`, n, languageName(lang), jsonArrayRule, metaBlock("About this text", meta), chunk)
}

const replySystem = `You are an expert at writing training data for language models. You read a reply (a letter, an answer, a response) and reconstruct the message it was answering.`

func replyPrompt(chunk string, meta map[string]string, n int, lang string) string {
	return fmt.Sprintf(`The text below is a reply that someone wrote. Write exactly %d distinct messages that a correspondent could have sent to receive this reply.

Rules:
- Write each message in the first person, in the correspondent's own voice, as the message itself (a letter, a note, a question), not as a description of one.
- Make it consistent with everything the reply responds to: the questions it answers, the news it reacts to, the requests it grants or refuses, the worries it addresses. Invent only what is needed to make the message coherent, and nothing that the reply contradicts.
- Do not quote the reply and do not mention it; the correspondent has not seen it yet.
- Give enough detail that the reply reads as a natural answer to the message on its own.
- Write the messages in %s.
%s

%sReply:
"""
%s
"""`, n, languageName(lang), jsonArrayRule, metaBlock("About this reply", meta), chunk)
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
