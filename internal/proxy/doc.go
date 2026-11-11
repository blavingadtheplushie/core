// Package proxy is an OpenAI/Anthropic-compatible LLM gateway: several
// upstreams (base URL + API key from the vault), automatic fallback when one
// fails (quota, 429, 5xx), model aliases mapped per upstream, streaming
// relayed as-is, and client API keys (sk-blv-…) stored as sha256 hashes.
package proxy
