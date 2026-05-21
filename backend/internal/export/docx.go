package export

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/model"
)

type docxPara struct {
	text   string
	bold   bool
	center bool
}

type docxCell struct {
	paras    []docxPara
	gridSpan int
	center   bool
}

func BuildResultDOCX(projects []model.ProjectRow, tasks []model.TaskRow) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	if err := writeDocxPart(zw, "[Content_Types].xml", contentTypesXML()); err != nil {
		return nil, err
	}
	if err := writeDocxPart(zw, "_rels/.rels", packageRelsXML()); err != nil {
		return nil, err
	}
	if err := writeDocxPart(zw, "word/document.xml", buildDocumentXML(projects, tasks)); err != nil {
		return nil, err
	}
	if err := writeDocxPart(zw, "word/_rels/document.xml.rels", documentRelsXML()); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeDocxPart(zw *zip.Writer, name, body string) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(body))
	return err
}

func buildDocumentXML(projects []model.ProjectRow, tasks []model.TaskRow) string {
	headers := []string{"№ п/п", "Инвестиционный проект", "Стадия", "Задача проекта", "Меры поддержки по проекту"}
	// Ширины в twips под A4 landscape с полями 720 twips:
	// полезная ширина = 16840 - 720 - 720 = 15400
	widths := []int{700, 4000, 4000, 3800, 2900}
	tasksByDeal := buildTasksByDeal(tasks)
	sections := groupBySection(projects)

	var body strings.Builder
	body.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="15400" w:type="dxa"/><w:tblLayout w:type="fixed"/><w:tblCellMar><w:top w:w="40" w:type="dxa"/><w:left w:w="90" w:type="dxa"/><w:bottom w:w="40" w:type="dxa"/><w:right w:w="90" w:type="dxa"/></w:tblCellMar><w:tblBorders>`)
	body.WriteString(`<w:top w:val="single" w:sz="6" w:space="0" w:color="000000"/>`)
	body.WriteString(`<w:left w:val="single" w:sz="6" w:space="0" w:color="000000"/>`)
	body.WriteString(`<w:bottom w:val="single" w:sz="6" w:space="0" w:color="000000"/>`)
	body.WriteString(`<w:right w:val="single" w:sz="6" w:space="0" w:color="000000"/>`)
	body.WriteString(`<w:insideH w:val="single" w:sz="6" w:space="0" w:color="000000"/>`)
	body.WriteString(`<w:insideV w:val="single" w:sz="6" w:space="0" w:color="000000"/>`)
	body.WriteString(`</w:tblBorders></w:tblPr>`)
	body.WriteString(`<w:tblGrid><w:gridCol w:w="700"/><w:gridCol w:w="4000"/><w:gridCol w:w="4000"/><w:gridCol w:w="3800"/><w:gridCol w:w="2900"/></w:tblGrid>`)

	headerCells := make([]docxCell, 0, len(headers))
	for _, h := range headers {
		headerCells = append(headerCells, docxCell{
			paras:  []docxPara{{text: h, bold: true, center: true}},
			center: true,
		})
	}
	body.WriteString(docxRowXML(headerCells, widths))

	num := 1
	for _, sec := range sections {
		if len(sec.projects) == 0 {
			continue
		}
		body.WriteString(docxRowXML([]docxCell{
			{paras: []docxPara{{text: "", center: true}}},
			{paras: []docxPara{{text: sec.name, bold: true, center: true}}, gridSpan: 4, center: true},
		}, widths))

		for _, p := range sec.projects {
			projectParas := []docxPara{
				{text: strings.TrimSpace(p.DealTitle), bold: true},
				{text: "Местоположение: " + strings.TrimSpace(p.Location)},
				{text: "Организация-инвестор: " + strings.TrimSpace(p.Investor)},
				{text: "Объём инвестиций: " + strings.TrimSpace(p.InvestPlan)},
				{text: "Срок реализации: " + strings.TrimSpace(p.DateRange)},
				{text: "Количество рабочих мест: " + strings.TrimSpace(p.Jobs)},
			}

			stageParas := []docxPara{
				{text: "Проектом предполагается:", bold: true},
			}
			for _, line := range splitLines(strings.TrimSpace(p.Description)) {
				stageParas = append(stageParas, docxPara{text: line})
			}
			stageParas = append(stageParas, docxPara{text: "Информация о стадии и ходе реализации:", bold: true})
			for _, line := range splitLines(strings.TrimSpace(p.Progress)) {
				stageParas = append(stageParas, docxPara{text: line})
			}

			taskParas := make([]docxPara, 0, 4)
			for _, line := range splitLines(tasksByDeal[p.DealID]) {
				taskParas = append(taskParas, docxPara{text: line})
			}
			supportParas := make([]docxPara, 0, 2)
			for _, line := range splitLines(strings.TrimSpace(p.Support)) {
				supportParas = append(supportParas, docxPara{text: line})
			}

			body.WriteString(docxRowXML([]docxCell{
				{paras: []docxPara{{text: fmt.Sprintf("%d", num), center: true}}, center: true},
				{paras: projectParas},
				{paras: stageParas},
				{paras: taskParas},
				{paras: supportParas},
			}, widths))
			num++
		}
	}

	body.WriteString(`</w:tbl>`)
	body.WriteString(`<w:p/>`)
	body.WriteString(`<w:sectPr><w:pgSz w:w="16840" w:h="11900" w:orient="landscape"/><w:pgMar w:top="720" w:right="720" w:bottom="720" w:left="720" w:header="450" w:footer="450" w:gutter="0"/></w:sectPr>`)

	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:wpc="http://schemas.microsoft.com/office/word/2010/wordprocessingCanvas" ` +
		`xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" ` +
		`xmlns:o="urn:schemas-microsoft-com:office:office" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" ` +
		`xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math" ` +
		`xmlns:v="urn:schemas-microsoft-com:vml" ` +
		`xmlns:wp14="http://schemas.microsoft.com/office/word/2010/wordprocessingDrawing" ` +
		`xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" ` +
		`xmlns:w10="urn:schemas-microsoft-com:office:word" ` +
		`xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" ` +
		`xmlns:w14="http://schemas.microsoft.com/office/word/2010/wordml" ` +
		`xmlns:wpg="http://schemas.microsoft.com/office/word/2010/wordprocessingGroup" ` +
		`xmlns:wpi="http://schemas.microsoft.com/office/word/2010/wordprocessingInk" ` +
		`xmlns:wne="http://schemas.microsoft.com/office/word/2006/wordml" ` +
		`xmlns:wps="http://schemas.microsoft.com/office/word/2010/wordprocessingShape" mc:Ignorable="w14 wp14">` +
		`<w:body>` + body.String() + `</w:body></w:document>`
}

func docxRowXML(cells []docxCell, widths []int) string {
	var b strings.Builder
	b.WriteString(`<w:tr>`)
	col := 0
	for _, c := range cells {
		span := c.gridSpan
		if span < 1 {
			span = 1
		}
		tcW := 0
		for i := 0; i < span && col+i < len(widths); i++ {
			tcW += widths[col+i]
		}
		col += span
		b.WriteString(`<w:tc><w:tcPr>`)
		if tcW > 0 {
			b.WriteString(fmt.Sprintf(`<w:tcW w:w="%d" w:type="dxa"/>`, tcW))
		}
		if c.gridSpan > 1 {
			b.WriteString(fmt.Sprintf(`<w:gridSpan w:val="%d"/>`, c.gridSpan))
		}
		b.WriteString(`<w:vAlign w:val="top"/></w:tcPr>`)
		if len(c.paras) == 0 {
			b.WriteString(`<w:p/>`)
		} else {
			for _, p := range c.paras {
				b.WriteString(docxParaXML(p.text, p.bold, p.center || c.center))
			}
		}
		b.WriteString(`</w:tc>`)
	}
	b.WriteString(`</w:tr>`)
	return b.String()
}

func docxParaXML(text string, bold bool, center bool) string {
	var b strings.Builder
	b.WriteString(`<w:p>`)
	b.WriteString(`<w:pPr>`)
	b.WriteString(`<w:spacing w:before="0" w:after="0" w:line="220" w:lineRule="auto"/>`)
	if center {
		b.WriteString(`<w:jc w:val="center"/>`)
	} else {
		b.WriteString(`<w:jc w:val="left"/>`)
	}
	b.WriteString(`</w:pPr>`)
	b.WriteString(`<w:r>`)
	if bold {
		b.WriteString(`<w:rPr><w:b/><w:sz w:val="20"/><w:rFonts w:ascii="Times New Roman" w:hAnsi="Times New Roman" w:cs="Times New Roman"/></w:rPr>`)
	} else {
		b.WriteString(`<w:rPr><w:sz w:val="20"/><w:rFonts w:ascii="Times New Roman" w:hAnsi="Times New Roman" w:cs="Times New Roman"/></w:rPr>`)
	}
	if text == "" {
		b.WriteString(`<w:t/>`)
	} else {
		escaped := xmlEscape(text)
		if strings.TrimSpace(text) != text {
			b.WriteString(`<w:t xml:space="preserve">` + escaped + `</w:t>`)
		} else {
			b.WriteString(`<w:t>` + escaped + `</w:t>`)
		}
	}
	b.WriteString(`</w:r></w:p>`)
	return b.String()
}

func splitLines(text string) []string {
	raw := strings.Split(text, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func xmlEscape(s string) string {
	s = stripInvalidXMLChars(s)
	repl := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return repl.Replace(s)
}

func stripInvalidXMLChars(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isValidXMLRune(r) {
			b.WriteRune(r)
			continue
		}
		// Сохраняем читабельность текста, подменяя мусорный символ пробелом.
		b.WriteRune(' ')
	}
	return b.String()
}

func isValidXMLRune(r rune) bool {
	switch r {
	case 0x9, 0xA, 0xD:
		return true
	}
	if r >= 0x20 && r <= 0xD7FF {
		return true
	}
	if r >= 0xE000 && r <= 0xFFFD {
		return true
	}
	return r >= 0x10000 && r <= 0x10FFFF
}

func contentTypesXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
		`</Types>`
}

func packageRelsXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
		`</Relationships>`
}

func documentRelsXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"></Relationships>`
}
