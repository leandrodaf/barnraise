package config

import (
	"fmt"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// compileTokens reads the sections that save tokens: outfilter and terse.
func (r *rawConfig) compileTokens(c *domain.ProjectConfig) error {
	c.Outfilter = domain.DefaultOutfilter()
	if v := strings.ToLower(strings.TrimSpace(r.Outfilter.Delegate)); v != "" {
		if !domain.ValidDelegate(v) {
			return fmt.Errorf("outfilter.delegate %q: use auto, rtk or off", r.Outfilter.Delegate)
		}
		c.Outfilter.Delegate = v
	}
	if v := r.Outfilter.Keep; v != "" {
		d, err := domain.ParseDuration(v)
		if err != nil || d <= 0 {
			return fmt.Errorf("outfilter.keep %q: use a duration such as 7d or 24h", v)
		}
		c.Outfilter.Keep = d
	}
	if v := r.Outfilter.KeepSize; v != "" {
		n, err := domain.ParseBytes(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("outfilter.keep_size %q: use a size such as 64MB", v)
		}
		c.Outfilter.KeepBytes = n
	}
	mode := strings.ToLower(strings.TrimSpace(r.Terse.Mode))
	if !domain.ValidTerse(mode) {
		return fmt.Errorf("terse.mode %q: use auto, all or off", r.Terse.Mode)
	}
	if mode == "" {
		mode = domain.TerseAuto
	}
	c.Terse = domain.TerseDef{Mode: mode, Steps: r.Terse.Steps}
	return nil
}
