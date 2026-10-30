package tools

import "testing"

func TestPickVoice(t *testing.T) {
	vs := []elevenVoice{{VoiceID: "aaaaaaaaaaaaaaaaaaaa", Name: "Rachel"}, {VoiceID: "bbbbbbbbbbbbbbbbbbbb", Name: "George"}}
	cases := map[string]string{"": "Rachel", "george": "George", "Geo": "George", "bbbbbbbbbbbbbbbbbbbb": "George", "cccccccccccccccccccc": "cccccccccccccccccccc"}
	for in, want := range cases {
		v, err := pickVoice(vs, in)
		if err != nil || v.Name != want {
			t.Errorf("pickVoice(%q) = %q, %v; want %q", in, v.Name, err, want)
		}
	}
	if _, err := pickVoice(vs, "nobody"); err == nil {
		t.Error("unknown voice must fail")
	}
}

func TestElevenToolsRegistered(t *testing.T) {
	d := vaultDeps(t)
	registerElevenLabs(d)
	for _, n := range []string{"tts_speak", "tts_voices"} {
		if _, ok := d.Reg.Get(n); !ok {
			t.Errorf("%s not registered", n)
		}
	}
}
