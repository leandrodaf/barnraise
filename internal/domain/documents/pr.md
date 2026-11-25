{{/* Body of the item's pull request. Copy it to .barnraise/documents/pr.md to change it. */ -}}
Barnraise item **{{.Item.ID}}** · kind {{.Item.Kind}} · {{.Item.Priority}}
{{- with .Item.Goal}} · goal {{.}}{{end}}
{{- with .Item.Area}} · area {{.}}{{end}}

{{with .Item.TrackerRef}}Tracker: {{.}}

{{end -}}
### Criteria
{{.Criteria}}
{{- if .Preserve}}
### What must not change
{{bullets .Preserve}}
{{end}}
{{- if .Tweaks}}
### Tweaks riding along
{{bullets .Tweaks}}
{{end}}
{{- if .Diagrams}}
### Before and after
{{range .Diagrams}}
{{with .Title}}**{{.}}**
{{end}}
{{mermaid .}}
{{end}}
{{- end}}
Step: `{{.Item.Step}}` · status: `{{.Item.Status}}`
