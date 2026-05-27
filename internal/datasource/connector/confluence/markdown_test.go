package confluence

import (
	"strings"
	"testing"
)

func TestStorageToMarkdown_BasicHTML(t *testing.T) {
	got := storageToMarkdown(`<h1>Title</h1><p><strong>Hello</strong> <em>world</em></p><ul><li>A</li><li>B</li></ul>`)
	want := "# Title\n\n**Hello** *world*\n\n- A\n- B\n"
	if got != want {
		t.Fatalf("markdown = %q, want %q", got, want)
	}
}

func TestStorageToMarkdown_ConfluenceMacros(t *testing.T) {
	got := storageToMarkdown(`
<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[fmt.Println("hi")]]></ac:plain-text-body></ac:structured-macro>
<ac:structured-macro ac:name="drawio"><ac:parameter ac:name="diagramName">System</ac:parameter></ac:structured-macro>
<ac:image><ri:attachment ri:filename="arch.png" /></ac:image>
`)
	for _, want := range []string{
		"```\nfmt.Println(\"hi\")\n```",
		"![System](attachments/System.png)",
		"![arch.png](attachments/arch.png)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("markdown missing %q in %q", want, got)
		}
	}
}
