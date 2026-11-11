package proxy

// candidate is one upstream to try, with the real model name to send.
type candidate struct {
	up    Upstream
	model string
}

// candidates lists, in priority order, the upstreams of the given style that
// can serve the requested model. Cooling-down upstreams go last (still tried
// if everything else failed).
func (p *Proxy) candidates(style, model string) []candidate {
	maps := map[string]string{} // upstream → model for this alias
	for _, m := range p.Mappings() {
		if m.Alias == model {
			maps[m.Upstream] = m.Model
		}
	}
	var up, down []candidate
	for _, u := range p.Upstreams() {
		if !u.Enabled || u.Style != style {
			continue
		}
		real, ok := maps[u.Name]
		if !ok {
			real, ok = maps["*"]
		}
		if !ok {
			if len(maps) > 0 {
				continue // alias exists but not on this upstream
			}
			if len(u.Models) > 0 && !contains(u.Models, model) {
				continue
			}
			real = model
		}
		c := candidate{up: u, model: real}
		if p.isDown(u.Name) {
			down = append(down, c)
		} else {
			up = append(up, c)
		}
	}
	return append(up, down...)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// retryable says whether a failed upstream answer should fall through to the
// next upstream: auth/quota/rate/overload/server errors. Plain 400s (bad
// request from the client) are returned as-is.
func retryable(status int, body []byte) bool {
	switch {
	case status == 401, status == 402, status == 403, status == 404, status == 408, status == 409, status == 429:
		return true
	case status >= 500:
		return true
	case status == 400:
		return quotaLike(body)
	}
	return false
}

func quotaLike(body []byte) bool {
	s := string(body)
	for _, w := range []string{"quota", "insufficient", "credit", "billing", "rate limit", "rate_limit", "overloaded", "exceeded", "balance"} {
		if containsFold(s, w) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	n := len(sub)
	for i := 0; i+n <= len(s); i++ {
		match := true
		for j := 0; j < n; j++ {
			a, b := s[i+j], sub[j]
			if 'A' <= a && a <= 'Z' {
				a += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
