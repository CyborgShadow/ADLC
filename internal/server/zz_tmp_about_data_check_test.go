package server

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

func TestTmpAboutDataFragment(t *testing.T) {
	src := `{{if eq .Page "about"}}{{with .Body}}` + aboutDataHTML + `{{end}}{{end}}`
	tp, err := template.New("x").Funcs(funcs).Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var buf bytes.Buffer
	err = tp.Execute(&buf, struct {
		Page string
		Body any
	}{"about", struct{ Data aboutDataInfo }{aboutDataFacts()}})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"adlc_event", "TAMPERED", "STALE PROJECTION", "segment.created", "adlc_loop_tick", "viewBox"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "ZgotmplZ") {
		t.Errorf("escaping problem")
	}
	t.Logf("%d bytes rendered", len(out))
}
