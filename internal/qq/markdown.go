package qq

import "regexp"

// QQ custom Markdown: group/C2C msg_type=2 with markdown.content.
// Plain text and media retain their own payloads; never retry uncertain sends
// using a different message type (that could duplicate a delivered message).
var markdownSyntax = regexp.MustCompile("(?m)(^ {0,3}(#{1,6} |>[ \\t]?|[-+*] |[0-9]+[.)] |```|~~~|(?:---+|\\*\\*\\*+|___+)[ \\t]*$))|(?:\\*\\*[^*\\n]+\\*\\*|__[^_\\n]+__|~~[^~\\n]+~~)|(?:^|[ \\t(（])(?:\\*[^*\\n]+\\*|_[^_\\n]+_)(?:$|[ \\t.,!?，。！？）)])|!?\\[[^]\\n]+\\]\\(https?://[^)\\s]+\\)|<https?://[^>\\s]+>|`[^`\\n]+`")
var escapedMarkdown = regexp.MustCompile(`\\[\\*_#>~\x60\[\]()]`)

func isMarkdown(text string) bool {
	return markdownSyntax.MatchString(escapedMarkdown.ReplaceAllString(text, "x"))
}

func textMessageBody(text string, markdown bool) map[string]any {
	if markdown {
		return map[string]any{"msg_type": 2, "markdown": map[string]any{"content": text}}
	}
	return map[string]any{"msg_type": 0, "content": text}
}
