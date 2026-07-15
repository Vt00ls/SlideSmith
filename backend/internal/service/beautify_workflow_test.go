package service

import "testing"

func TestBeautifySpecPageDeclaredAcceptsCanonicalMarkdownEntries(t *testing.T) {
	for _, markdown := range []string{
		"P01 | source_slide=1\n",
		"## P01\n",
		"### P01 Lock\n",
		"  ###### P01: cover\n",
		"- P01: source_slide=1\n",
		"  * P01 | output_page=1\n",
	} {
		if !beautifySpecPageDeclared(markdown, "P01") {
			t.Fatalf("canonical page declaration rejected: %q", markdown)
		}
	}
}

func TestBeautifySpecPageDeclaredRejectsNarrativeMentions(t *testing.T) {
	for _, markdown := range []string{
		"Page mapping: P01 maps to source slide 1\n",
		"- resource image.p01 | P01 | purpose=cover\n",
		"## Prefix P01\n",
	} {
		if beautifySpecPageDeclared(markdown, "P01") {
			t.Fatalf("narrative page mention accepted: %q", markdown)
		}
	}
}

func TestBeautifySpecTextBlockPreservedUsesFrozenParagraphUnits(t *testing.T) {
	block := BeautifyInventoryText{
		ID:         "s02_sh3",
		Text:       "保持页面数量与顺序。\nPreserve every visible sentence.\n数据与标点不得改变：99.9%。",
		Paragraphs: []string{"保持页面数量与顺序。", "Preserve every visible sentence.", "数据与标点不得改变：99.9%。"},
	}
	markdown := "- s02_sh3 paragraphs:\n  - \"保持页面数量与顺序。\"\n  - \"Preserve every visible sentence.\"\n  - \"数据与标点不得改变：99.9%。\"\n"
	if !beautifySpecTextBlockPreserved(markdown, block) {
		t.Fatal("verbatim paragraph list was rejected")
	}
	if beautifySpecTextBlockPreserved("保持页面数量与顺序。\nPreserve every visible sentence.\n数据与标点不得改变：99.8%。", block) {
		t.Fatal("changed frozen punctuation/data was accepted")
	}
}

func TestBeautifySpecTextBlockPreservedFallsBackToWholeText(t *testing.T) {
	block := BeautifyInventoryText{ID: "s01_sh1", Text: "Single frozen unit"}
	if !beautifySpecTextBlockPreserved("text=Single frozen unit", block) {
		t.Fatal("single frozen text unit was rejected")
	}
	if beautifySpecTextBlockPreserved("text=changed", block) {
		t.Fatal("changed single frozen text unit was accepted")
	}
}
