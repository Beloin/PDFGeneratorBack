package service

import (
	"encoding/json"
	"fmt"
	"strings"
)

func templateType(id string) string {
	if strings.HasSuffix(id, "-pptx") {
		return "slideshow"
	}
	return "pdf"
}

// slideshowScript produces a complete editable source script for each template.
func slideshowScript(title, subtitle string, sections [][2]string) string {
	data, _ := json.Marshal(sections)
	return fmt.Sprintf(`from slides import Slides

deck = Slides(OUT_PATH)
deck.slide(background="#0A0F16")
deck.rect("cover-accent", 64, 112, 72, 6, fill="#3EE6D0")
deck.text(%q, "cover-title", 64, 160, 832, 132, font_size=48, color="#E9F0F7", bold=True)
deck.text(%q, "cover-subtitle", 64, 326, 800, 92, font_size=22, color="#9FB0C0")

sections = %s
for index, (title, body) in enumerate(sections, start=1):
    deck.slide(background="#0A0F16")
    deck.text(f"{index:02d}", f"slide{index}-number", 64, 40, 100, 32, font_size=16, color="#3EE6D0")
    deck.text(title, f"slide{index}-title", 64, 92, 832, 84, font_size=36, color="#E9F0F7", bold=True)
    deck.rect(f"slide{index}-card", 64, 210, 832, 230, fill="#121B27")
    deck.text(body, f"slide{index}-body", 88, 240, 784, 174, font_size=24, color="#9FB0C0")
deck.save()
`, title, subtitle, data)
}
