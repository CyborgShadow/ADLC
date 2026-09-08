package server

import (
	"bytes"
	"html/template"
	"os"
	"testing"
)

func TestTmpRenderPreview(t *testing.T) {
	dst := os.Getenv("ADLC_PREVIEW_OUT")
	if dst == "" {
		t.Skip("no out")
	}
	src := `{{if eq .Page "about"}}{{with .Body}}` + aboutDataHTML + `{{end}}{{end}}`
	tp := template.Must(template.New("x").Funcs(funcs).Parse(src))
	var buf bytes.Buffer
	if err := tp.Execute(&buf, struct {
		Page string
		Body any
	}{"about", struct{ Data aboutDataInfo }{aboutDataFacts()}}); err != nil {
		t.Fatal(err)
	}
	base := `:root{--bg:#0f1417;--card:#161d21;--line:#253136;--ink:#dde6e6;--dim:#8fa3a5;
--ok:#5fbf8b;--warn:#d8a750;--bad:#e0796d;--live:#59a9c4;--accent:#4bb3a8}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.55 system-ui,sans-serif}
main{padding:22px 24px 60px;max-width:1220px;margin:0 auto}
h2{font-size:14px;margin:26px 0 10px;font-weight:600}
.grid{display:grid;gap:10px;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));margin-bottom:6px}
.card{background:var(--card);border:1px solid var(--line);border-radius:4px;padding:13px 15px}
.card .n{font-size:24px;font-weight:600;line-height:1.1}
.card .l{color:var(--dim);font-size:12px;margin-top:3px}
table{width:100%;border-collapse:collapse;background:var(--card);border:1px solid var(--line);border-radius:4px}
th{text-align:left;font-size:11px;text-transform:uppercase;letter-spacing:.07em;color:var(--dim);padding:7px 11px;border-bottom:1px solid var(--line)}
td{padding:7px 11px;border-bottom:1px solid var(--line);vertical-align:top}
.wrap{overflow-x:auto;margin-bottom:6px}
.mono{font-family:ui-monospace,Consolas,monospace;font-size:12px}
.pill{display:inline-block;padding:1px 8px;border-radius:10px;font-size:11px;font-weight:600}
.ok{background:#12301f;color:var(--ok)}.warn{background:#31260d;color:var(--warn)}
.bad{background:#331915;color:var(--bad)}.mute{background:var(--line);color:var(--dim)}
.dim{color:var(--dim)}.small{font-size:12px}
`
	html := "<!doctype html><meta charset=utf-8><style>" + base + aboutCSS + aboutDataCSS + "</style><main>" + buf.String() + "</main>"
	if err := os.WriteFile(dst, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
}
