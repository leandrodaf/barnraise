package config

import (
	"testing"
)

func FuzzCompile(f *testing.F) {
	for _, s := range []string{
		"", "template: blank\nproject: x\n", "template: mobile-android\nlanguage: pt-BR\nchecks:\n  main: scripts/verificar.py\n",
		"capacity: { goal: 70%, bugs: 20%, improvements: 10% }\nself_improvement: { budget: 2/week }\n",
		"flows:\n  feature:\n    steps: [ {name: a, type: agent, role: implementer} ]\n",
		"[1, 2", "- a\n- b\n", "a: &x [*x]\n", "template: [x]\n", "roles: 3\nchecks: [a]\nareas: x\n",
		"lease: { renew: 99d, ttl: -1s }\n", "target_size: 6h-2h\n", "models: { classes: { simple: [ \"bad\" ] } }\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := Compile(t.TempDir(), data)
		if err == nil && cfg != nil {
			_ = cfg.Validate()
		}
	})
}
